package share

import (
	"fmt"

	"github.com/lesomnus/darak/internal/statefile"
)

// NewFileStore loads a store from path, or starts empty if it does not exist,
// and coordinates every change on it across replicas.
//
// A missing file is not an error: the first run has no links. A malformed one
// IS, because silently starting empty would revoke every live link without
// saying so — the initial load through guard surfaces that at startup.
//
// The file lives at an operator-configured path outside the served tree, and
// every read reloads it when another replica has written while every write is a
// locked read-modify-write: internal/statefile is where that path is resolved
// and that protocol lives, which is why this file no longer touches the
// filesystem itself.
func NewFileStore(path string) (*Store, error) {
	s := NewStore()
	s.guard = statefile.New(path)
	// The initial load goes through guard so it both catches a malformed file at
	// startup (the reason a missing file is fine but a broken one is not) and
	// primes the reload cache.
	s.mu.Lock()
	err := s.guard.Fresh(s.loadLocked)
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("share: %s: %w", path, err)
	}
	return s, nil
}
