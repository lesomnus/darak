package share

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Two file-backed stores over one path stand in for two replicas mounting the
// same state volume. The guarantees are the point of internal/statefile, checked
// here against the real share.Store rather than the toy in statefile's own test.
func TestReplicasShareTheSameLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.json")
	a, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}

	// A link created on one replica resolves on the other — the failure a naive
	// per-replica cache would produce (create here, 404 there).
	l, err := a.Create("alice", "homes/alice/f", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Resolve(l.Token, ""); err != nil {
		t.Fatalf("replica B could not resolve a link replica A created: %v", err)
	}

	// Revoking on one replica is seen on the other.
	if err := a.Revoke("alice", l.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Resolve(l.Token, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replica B still resolves a revoked link: %v", err)
	}
}

func TestConcurrentReplicaWritesBothSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.json")
	a, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}

	// Each replica has loaded the (empty) start, then each creates a link. Without
	// reload-before-write, whichever wrote second would erase the other's link.
	la, err := a.Create("alice", "homes/alice/a", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lb, err := b.Create("bob", "homes/bob/b", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// A third reader sees both.
	c, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(la.Token, ""); err != nil {
		t.Errorf("alice's link was lost: %v", err)
	}
	if _, err := c.Resolve(lb.Token, ""); err != nil {
		t.Errorf("bob's link was lost: %v", err)
	}
}
