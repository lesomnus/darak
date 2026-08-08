//darak:local-state — Statfs on the OPERATOR-SUPPLIED data root, never on a
// path from a request. The lint check in internal/lint forbids path-resolving
// syscalls in the server because they re-run permission resolution against the
// calling process, which is root; that reasoning does not reach here, because
// the only path this file resolves is the -root flag and the answer it returns
// is a byte count for the filesystem as a whole. No request can steer it.

package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Capacity is the state of the filesystem holding the data.
type Capacity struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`

	// TotalInodes and FreeInodes matter on a server whose users keep many small
	// files: running out of them fails writes while df still shows free space.
	TotalInodes uint64 `json:"total_inodes"`
	FreeInodes  uint64 `json:"free_inodes"`
}

// Capacity reports free space on the data tree.
//
// FreeBytes is Bavail, not Bfree — the space available to a non-root writer,
// which is what a user is about to run out of. Reporting Bfree would show the
// reserve only root can use and overstate what anyone else can write.
func (a *Admin) Capacity() (*Capacity, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(a.cfg.Root, &st); err != nil {
		return nil, fmt.Errorf("admin: statfs %s: %w", a.cfg.Root, err)
	}
	bs := uint64(st.Bsize)
	return &Capacity{
		Path:        a.cfg.Root,
		TotalBytes:  st.Blocks * bs,
		FreeBytes:   st.Bavail * bs,
		UsedBytes:   (st.Blocks - st.Bfree) * bs,
		TotalInodes: st.Files,
		FreeInodes:  st.Ffree,
	}, nil
}

// usageCache holds the last per-uid usage measurement.
//
// Usage is served from here and refreshed in the background because measuring
// it is the one operation in this package that scales with the size of the
// data rather than the number of accounts. Computing it inside a request would
// mean the page hangs exactly when the disk is full and slow, which is when
// someone is most likely to be looking at it.
type usageCache struct {
	mu      sync.RWMutex
	byUID   map[uint32]uint64
	at      time.Time
	source  string
	lastErr string
}

func newUsageCache() *usageCache {
	return &usageCache{byUID: map[uint32]uint64{}}
}

func (c *usageCache) get(uid uint32) (uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.byUID[uid]
	return n, ok
}

func (c *usageCache) snapshot() (map[uint32]uint64, time.Time, string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[uint32]uint64, len(c.byUID))
	for k, v := range c.byUID {
		out[k] = v
	}
	return out, c.at, c.source, c.lastErr
}

func (c *usageCache) store(byUID map[uint32]uint64, source string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastErr = err.Error()
		// Keep the previous numbers. A stale measurement labelled with its age
		// is more useful to an operator than an empty table.
		return
	}
	c.byUID, c.at, c.source, c.lastErr = byUID, time.Now(), source, ""
}

// UsageReport is what the page shows about per-user consumption.
type UsageReport struct {
	// Source names how the numbers were obtained, because the two differ in
	// what they mean: "zfs" counts everything the uid owns in the dataset
	// including snapshots' referenced blocks, "du" counts what is reachable
	// under each home right now.
	Source string `json:"source"`
	// MeasuredAt is zero when nothing has been measured yet.
	MeasuredAt time.Time         `json:"measured_at"`
	ByUID      map[string]uint64 `json:"by_uid"`
	Error      string            `json:"error,omitempty"`
}

// Usage returns the cached per-uid usage.
func (a *Admin) Usage() *UsageReport {
	byUID, at, source, lastErr := a.usage.snapshot()
	out := make(map[string]uint64, len(byUID))
	for uid, n := range byUID {
		out[strconv.FormatUint(uint64(uid), 10)] = n
	}
	return &UsageReport{Source: source, MeasuredAt: at, ByUID: out, Error: lastErr}
}

// RefreshUsage measures per-uid usage. Call it from housekeeping, not from a
// handler.
//
// ZFS is asked first because it already knows: the filesystem maintains a
// per-uid byte count, so the answer is a table lookup regardless of how many
// files there are. Walking with du is the fallback for anything else, and it
// costs one traversal of every home.
func (a *Admin) RefreshUsage(ctx context.Context) {
	if byUID, err := a.zfsUserspace(ctx); err == nil {
		a.usage.store(byUID, "zfs", nil)
		return
	}
	byUID, err := a.duUsage(ctx)
	a.usage.store(byUID, "du", err)
}

// zfsUserspace reads the per-uid accounting ZFS maintains.
//
// -H is tab-separated with no header, -p is exact bytes rather than the human
// units the default prints, and -n asks for the numeric uid so a name that no
// longer resolves is still attributed to the right number — which is the whole
// premise the roster rests on.
func (a *Admin) zfsUserspace(ctx context.Context) (map[uint32]uint64, error) {
	out, err := a.cfg.Runner.Run(ctx, "", "zfs", "userspace", "-Hpn", "-o", "name,used", a.cfg.Root)
	if err != nil {
		return nil, err
	}
	byUID := map[uint32]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		uid, err := strconv.ParseUint(f[0], 10, 32)
		if err != nil {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		byUID[uint32(uid)] += n
	}
	if len(byUID) == 0 {
		return nil, fmt.Errorf("admin: zfs userspace reported nothing for %s", a.cfg.Root)
	}
	return byUID, nil
}

// duUsage attributes bytes by owning uid across the whole data tree.
//
// `du --inodes`-style per-owner accounting does not exist, so this asks find to
// print each file's uid and size and sums them. It counts the whole tree, not
// just homes, because a team folder is where a user's data most often is and
// leaving it out would make the number quietly wrong rather than absent.
//
// -xdev keeps it inside one filesystem, and %b is 512-byte blocks ALLOCATED
// rather than apparent size, so a sparse file is not reported as the space it
// would take if it were filled in.
func (a *Admin) duUsage(ctx context.Context) (map[uint32]uint64, error) {
	out, err := a.cfg.Runner.Run(ctx, "", "find", a.cfg.Root, "-xdev", "-type", "f", "-printf", "%U %b\n")
	if err != nil && out == "" {
		return nil, err
	}
	byUID := map[uint32]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		uidStr, blocksStr, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		uid, err := strconv.ParseUint(uidStr, 10, 32)
		if err != nil {
			continue
		}
		blocks, err := strconv.ParseUint(blocksStr, 10, 64)
		if err != nil {
			continue
		}
		byUID[uint32(uid)] += blocks * 512
	}
	// A partial result is normal: find reports permission errors to stderr and
	// keeps going, and it never reaches into a mount it was told to skip.
	return byUID, nil
}
