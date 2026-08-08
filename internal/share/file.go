//darak:local-state
//
// This file resolves a path, which the rest of the server must never do. The
// exemption is narrow and deliberate: the path comes from a command-line flag,
// not from a request, and it names server state rather than anything in the
// tree being served. There is no user whose helper could open it — that is the
// whole reason it lives outside the data volume, which nas-design.md §7 requires
// so the volume stays free of application state before it becomes a shared
// filesystem mounted by several gateways.
//
// See internal/lint for the check this marker is read by.

package share

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// NewFileStore loads a store from path, or starts empty if it does not exist,
// and persists every change back to it.
//
// A missing file is not an error: the first run has no links. A malformed one
// IS, because silently starting empty would revoke every live link without
// saying so.
func NewFileStore(path string) (*Store, error) {
	s := NewStore()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := s.Load(data); err != nil {
			return nil, fmt.Errorf("share: %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("share: %s: %w", path, err)
	}

	s.Save = func([]Link) error { return s.writeTo(path) }
	return s, nil
}

// writeTo renders the store and replaces path atomically.
//
// Temp file then rename, for the same reason the file protocol uses it: a
// partial write here would be a truncated JSON document, and the next start
// would refuse to load it — turning a crash mid-save into every link being
// unusable.
func (s *Store) writeTo(path string) error {
	data, err := s.Marshal()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("share: %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".shares-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename has happened

	// 0600: the file holds link tokens, and a token is the whole credential.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
