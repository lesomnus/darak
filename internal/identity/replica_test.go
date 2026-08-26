package identity

import (
	"path/filepath"
	"testing"
)

// Two file-backed stores over one path stand in for two replicas. An approval on
// one must authenticate a sign-in on the other, and two approvals made against
// different replicas must both survive rather than one clobbering the other.
func TestMappingReplicasAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	a, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Approve("alice", "https://idp", "obj-a", []string{"alice@example.com"}, "admin", now); err != nil {
		t.Fatal(err)
	}
	// The sign-in path on replica B must see it.
	if got, ok := b.BySubject("https://idp", "obj-a"); !ok || got != "alice" {
		t.Fatalf("replica B does not see alice's approval: %q, %v", got, ok)
	}

	// A concurrent approval on B, made from B's own (now-refreshed) view, must not
	// erase alice.
	if _, err := b.Approve("bob", "https://idp", "obj-b", []string{"bob@example.com"}, "admin", now); err != nil {
		t.Fatal(err)
	}
	c, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.BySubject("https://idp", "obj-a"); !ok {
		t.Error("alice was lost when bob was approved on another replica")
	}
	if _, ok := c.BySubject("https://idp", "obj-b"); !ok {
		t.Error("bob's approval did not persist")
	}
}

func TestQueueReplicasAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	a, err := NewFileQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFileQueue(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Record(req("obj-a", "a@example.com"), now); err != nil {
		t.Fatal(err)
	}
	if err := b.Record(req("obj-b", "b@example.com"), now); err != nil {
		t.Fatal(err)
	}
	// Both requests survive; neither replica's write erased the other's.
	if _, ok := b.Get("https://idp", "obj-a"); !ok {
		t.Error("request from replica A was lost")
	}
	if _, ok := a.Get("https://idp", "obj-b"); !ok {
		t.Error("request from replica B was lost")
	}
}
