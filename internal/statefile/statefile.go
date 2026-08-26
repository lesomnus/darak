// Package statefile serializes read-modify-write on a JSON state file that more
// than one process shares.
//
// darak runs as several replicas mounting the same state volume — nas-design.md
// §7 keeps this state OFF the data volume precisely so it can be shared this way.
// Each replica keeps a file's contents in memory for speed, which on its own
// would introduce two bugs the moment a second replica exists:
//
//   - a reader would not see the other replica's writes, and
//   - a writer would rewrite the whole file from a stale snapshot and erase them.
//
// A Guard closes both. Fresh reloads the in-memory copy when the file changed on
// disk (one stat when nothing did), so a read reflects another replica's write.
// Write takes an advisory lock across the whole read-modify-write and reloads
// inside it, so the version a replica rewrites is the version another replica
// just committed, never an older one it happened to hold.
//
// The Guard adds only the cross-process half of the coordination: the caller
// still serializes its own in-process access (its store mutex) and passes load
// and marshal callbacks that assume that lock is held. load rebuilds the caller's
// state from the file WHOLESALE, which is what lets a reload replace a stale
// snapshot without leaving indexes half-updated — the same property internal/
// server/epoch.go relies on, generalized here for the richer share and identity
// stores.
//
//darak:local-state
package statefile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Guard coordinates access to one state file across replicas.
//
// It holds no lock of its own: the caller serializes calls with the store mutex
// it already has, and the Guard only tracks what it last saw on disk so Fresh
// can skip a reload when nothing changed. A nil *Guard is valid and means "no
// shared file configured" — Fresh and Write become in-memory no-ops around the
// caller's own mutate — which is what tests and any single-replica-without-a-
// -file setup use.
type Guard struct {
	path string

	// What the last load saw, so a stat is enough to decide a reload is needed.
	// Size is compared alongside mtime because a coarse (1s) mtime can miss a
	// same-second rewrite, and every write here changes the length or does not
	// change the file at all.
	mtime  time.Time
	size   int64
	loaded bool
}

// New returns a Guard for the file at path. A nil Guard (New is never asked to
// produce one; callers pass nil directly) disables coordination.
func New(path string) *Guard { return &Guard{path: path} }

// Fresh reloads the caller's state via load if the file changed since the last
// load or write. The caller must hold its own lock. load is called with the
// file's bytes, or nil when the file does not exist yet.
//
// A nil Guard loads once (so an empty store is initialized) and never again.
func (g *Guard) Fresh(load func([]byte) error) error {
	if g == nil || g.path == "" {
		return nil
	}
	fi, err := os.Stat(g.path)
	switch {
	case os.IsNotExist(err):
		if g.loaded {
			return nil
		}
		if err := load(nil); err != nil {
			return err
		}
		g.loaded = true
		return nil
	case err != nil:
		return fmt.Errorf("statefile: stat %s: %w", g.path, err)
	}
	if g.loaded && fi.ModTime().Equal(g.mtime) && fi.Size() == g.size {
		return nil
	}
	data, err := os.ReadFile(g.path)
	if err != nil {
		return fmt.Errorf("statefile: read %s: %w", g.path, err)
	}
	if err := load(data); err != nil {
		return err
	}
	g.mtime, g.size, g.loaded = fi.ModTime(), fi.Size(), true
	return nil
}

// Seen records the file's current mtime and size as already loaded, so the next
// Fresh does not reload it. It is for a caller that loaded the file itself — or
// deliberately tolerated a failure to load it, as the request queue does with a
// corrupt file — and wants the Guard's reload cache to reflect that rather than
// re-reading the same bytes (or re-hitting the same parse error) on every read.
func (g *Guard) Seen() {
	if g == nil || g.path == "" {
		return
	}
	if fi, err := os.Stat(g.path); err == nil {
		g.mtime, g.size = fi.ModTime(), fi.Size()
	}
	g.loaded = true
}

// Write runs a read-modify-write under an exclusive cross-process lock: it
// reloads the newest committed state (via load) so mutate builds on it rather
// than on whatever snapshot this replica happened to hold, runs mutate, then —
// only if mutate reports it changed something — atomically replaces the file
// with marshal()'s bytes. The caller must hold its own lock.
//
// mutate returns whether a write is needed: a Revoke of an unknown token or a
// Sweep with nothing expired reports false, and the file (and its mtime, which
// every other replica watches) is left untouched.
//
// A nil Guard just runs mutate: there is no file to lock or rewrite.
func (g *Guard) Write(load func([]byte) error, mutate func() (bool, error), marshal func() ([]byte, error)) error {
	if g == nil || g.path == "" {
		_, err := mutate()
		return err
	}

	unlock, err := lock(g.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	// Reload before mutating: another replica may have committed since this one
	// last read, and rewriting the file from an older snapshot is exactly the
	// data loss this exists to prevent.
	if err := g.Fresh(load); err != nil {
		return err
	}
	write, err := mutate()
	if err != nil {
		return err
	}
	if !write {
		return nil
	}
	data, err := marshal()
	if err != nil {
		return err
	}
	if err := replace(g.path, data); err != nil {
		return err
	}
	// Our own write moved the file on; record what it now looks like so the next
	// Fresh does not reload bytes we already hold.
	if fi, err := os.Stat(g.path); err == nil {
		g.mtime, g.size, g.loaded = fi.ModTime(), fi.Size(), true
	}
	return nil
}

// lock takes an exclusive advisory (flock) lock on lockPath, creating it if
// needed, and returns a function that releases it. The lock file is separate
// from the state file so the state file can be replaced by rename underneath a
// held lock — the descriptor keeps the lock, not the path.
func lock(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("statefile: %s: %w", filepath.Dir(lockPath), err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("statefile: open lock %s: %w", lockPath, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("statefile: lock %s: %w", lockPath, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

// replace writes data to path atomically: temp file, fsync, rename. A partial
// write would leave a truncated JSON document that the next start refuses to
// load, so the file is only ever swapped whole. 0600 because these files hold
// credentials (link tokens) or decide who may sign in as whom.
func replace(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("statefile: %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("statefile: %s: %w", dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename has happened

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Durability before visibility: the rename must not publish a name whose
	// contents are still only in the page cache.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
