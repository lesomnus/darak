package statefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// store is a tiny caller: a map behind the load/marshal callbacks, standing in
// for the share and identity stores.
type store struct {
	g *Guard
	m map[string]string
}

func newStore(path string) *store {
	return &store{g: New(path), m: map[string]string{}}
}

func (s *store) load(data []byte) error {
	s.m = map[string]string{}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.m)
}

func (s *store) marshal() ([]byte, error) { return json.Marshal(s.m) }

func (s *store) set(k, v string) error {
	return s.g.Write(s.load, func() (bool, error) { s.m[k] = v; return true, nil }, s.marshal)
}

func (s *store) get(k string) (string, error) {
	if err := s.g.Fresh(s.load); err != nil {
		return "", err
	}
	return s.m[k], nil
}

func TestFreshSeesAnotherReplicasWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := newStore(path)
	b := newStore(path)

	if err := a.set("k", "v1"); err != nil {
		t.Fatal(err)
	}
	// b never loaded this; Fresh must pull it from disk.
	if got, err := b.get("k"); err != nil || got != "v1" {
		t.Fatalf("b.get = %q, %v; want v1", got, err)
	}
}

func TestWriteDoesNotClobberAConcurrentWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := newStore(path)
	b := newStore(path)

	// Both replicas load the (empty) starting state, then each writes a
	// different key. Without reload-before-write, whichever wrote second would
	// erase the other's key.
	if _, err := a.get("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.get("y"); err != nil {
		t.Fatal(err)
	}
	if err := a.set("x", "1"); err != nil {
		t.Fatal(err)
	}
	if err := b.set("y", "2"); err != nil {
		t.Fatal(err)
	}

	// The file on disk must hold both.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	final := map[string]string{}
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatal(err)
	}
	if final["x"] != "1" || final["y"] != "2" {
		t.Fatalf("file = %v; want both x=1 and y=2", final)
	}
}

func TestFreshSkipsReloadWhenUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := newStore(path)
	if err := a.set("k", "v"); err != nil {
		t.Fatal(err)
	}
	// Corrupt the in-memory map behind Fresh's back, then Fresh with no on-disk
	// change: it must NOT reload (the point of the mtime/size shortcut), so the
	// corruption is still visible. This pins the shortcut as a shortcut.
	a.m["k"] = "corrupt"
	if got, _ := a.get("k"); got != "corrupt" {
		t.Fatalf("Fresh reloaded despite no change: got %q", got)
	}
}

func TestNilGuardIsInMemory(t *testing.T) {
	s := &store{g: nil, m: map[string]string{}}
	if err := s.set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.get("k"); err != nil || got != "v" {
		t.Fatalf("nil-guard get = %q, %v; want v", got, err)
	}
}

func TestFreshInitializesEmptyStore(t *testing.T) {
	// A path that does not exist yet: the first Fresh must load(nil) so the store
	// starts empty rather than never being initialized.
	path := filepath.Join(t.TempDir(), "missing.json")
	a := newStore(path)
	a.m["stale"] = "should be cleared by load(nil)"
	if got, err := a.get("stale"); err != nil || got != "" {
		t.Fatalf("get on missing file = %q, %v; want empty", got, err)
	}
}
