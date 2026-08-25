// The session-epoch file and the cookie-signing key live at operator-supplied
// paths, for state this server owns; no user's credentials gate them, so they do
// not (and cannot) go through the per-user helper.
//
//darak:local-state
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ReadCookieKey reads the HMAC session-signing key from an operator-supplied
// file, trimming surrounding whitespace. An empty path returns a nil key, which
// makes Sessions fall back to a random per-process key (single replica only). A
// key under 16 bytes is refused — that is not a signing key.
func ReadCookieKey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cookie key: %w", err)
	}
	b = bytes.TrimSpace(b)
	if len(b) < 16 {
		return nil, fmt.Errorf("cookie key in %s is too short (%d bytes); need at least 16", path, len(b))
	}
	return b, nil
}

// epochStore holds a per-user "sessions not valid before" time on a shared file,
// so every replica agrees. A password change bumps the user's epoch to now, which
// makes every cookie issued earlier stop verifying — the stateless equivalent of
// closing that user's other sessions.
//
// Reads are cached and refreshed only when the file's mtime moves, so the request
// path is a map lookup under a mutex, not a file read. Writes take an exclusive
// flock and replace the file atomically (temp + rename), so two replicas' bumps
// cannot lose one and a reader never sees a half-written map.
//
// A nil *epochStore is valid and means "no shared state configured": Sessions
// then skips the epoch check and DeleteOthers is a no-op (single-replica mode).
type epochStore struct {
	path string

	mu    sync.Mutex
	m     map[string]int64
	mtime time.Time
}

func newEpochStore(path string) *epochStore {
	return &epochStore{path: path, m: map[string]int64{}}
}

// notBefore returns the unix time before which user's cookies are invalid, or 0.
func (e *epochStore) notBefore(user string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fi, err := os.Stat(e.path); err == nil && !fi.ModTime().Equal(e.mtime) {
		if b, err := os.ReadFile(e.path); err == nil {
			m := map[string]int64{}
			if json.Unmarshal(b, &m) == nil {
				e.m, e.mtime = m, fi.ModTime()
			}
		}
	}
	return e.m[user]
}

// bump sets user's not-before to at, under a cross-replica exclusive lock.
func (e *epochStore) bump(user string, at time.Time) error {
	lock, err := os.OpenFile(e.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	// Read-modify-write the whole map while holding the lock, so a concurrent
	// bump on another replica is serialised rather than lost.
	m := map[string]int64{}
	if b, err := os.ReadFile(e.path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	m[user] = at.UnixMilli() // milliseconds, to match a cookie's iat
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.path); err != nil {
		return err
	}

	// Refresh this replica's cache so the bump is visible here at once, not only
	// after the next mtime change.
	e.mu.Lock()
	e.m = m
	if fi, err := os.Stat(e.path); err == nil {
		e.mtime = fi.ModTime()
	}
	e.mu.Unlock()
	return nil
}
