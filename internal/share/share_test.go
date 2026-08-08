package share

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T, at time.Time) *Store {
	t.Helper()
	s := NewStore()
	s.Now = func() time.Time { return at }
	return s
}

func TestCreateAndResolve(t *testing.T) {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	s := newStore(t, at)

	l, err := s.Create("alice", "teams/design/plan.pdf", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if l.Token == "" || l.Owner != "alice" || l.Path != "teams/design/plan.pdf" {
		t.Fatalf("link = %#v", l)
	}
	if l.Protected() {
		t.Error("no password was set")
	}

	got, err := s.Resolve(l.Token, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "alice" || got.Path != l.Path {
		t.Errorf("resolved = %#v", got)
	}
}

// An unknown token, an expired one and a revoked one are one error. Telling them
// apart would confirm that a token once existed, which is the only thing a
// guesser learns from trying.
func TestUnknownExpiredAndRevokedAreIndistinguishable(t *testing.T) {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	s := newStore(t, at)

	live, _ := s.Create("alice", "homes/alice/a", "", time.Hour)
	revoked, _ := s.Create("alice", "homes/alice/b", "", time.Hour)
	if err := s.Revoke("alice", revoked.Token); err != nil {
		t.Fatal(err)
	}
	expired, _ := s.Create("alice", "homes/alice/c", "", time.Hour)
	s.Now = func() time.Time { return at.Add(2 * time.Hour) }

	for name, token := range map[string]string{
		"never existed": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"revoked":       revoked.Token,
		"expired":       expired.Token,
	} {
		if _, err := s.Resolve(token, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	// The live one is also expired by now, so nothing resolves.
	if _, err := s.Resolve(live.Token, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired live link: err = %v, want ErrNotFound", err)
	}
}

func TestPassword(t *testing.T) {
	s := newStore(t, time.Now())
	l, err := s.Create("alice", "homes/alice/x", "s3cret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Protected() {
		t.Fatal("link should be protected")
	}

	if _, err := s.Resolve(l.Token, "s3cret"); err != nil {
		t.Errorf("correct password: %v", err)
	}
	for _, wrong := range []string{"", "s3cre", "s3crett", "S3cret"} {
		if _, err := s.Resolve(l.Token, wrong); !errors.Is(err, ErrPassword) {
			t.Errorf("password %q: err = %v, want ErrPassword", wrong, err)
		}
	}
}

// The store is written to disk, so it must not hold the password itself.
func TestPasswordIsNotStoredInTheClear(t *testing.T) {
	s := newStore(t, time.Now())
	const pw = "hunter2-unmistakable"
	if _, err := s.Create("alice", "homes/alice/x", pw, time.Hour); err != nil {
		t.Fatal(err)
	}
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), pw) {
		t.Fatalf("the persisted store contains the password:\n%s", data)
	}
}

// Listing a user's own links must not hand back the material that verifies them.
func TestListDoesNotLeakTheHash(t *testing.T) {
	s := newStore(t, time.Now())
	if _, err := s.Create("alice", "homes/alice/x", "pw", time.Hour); err != nil {
		t.Fatal(err)
	}
	links := s.ListByOwner("alice")
	if len(links) != 1 {
		t.Fatalf("got %d links", len(links))
	}
	if links[0].PasswordHash != nil || links[0].PasswordSalt != nil {
		t.Error("the stored hash must not be returned to a caller")
	}
}

// A link is a capability its owner hands out; someone else must not be able to
// take it back, and must not learn whether it exists by trying.
func TestOnlyTheOwnerCanRevoke(t *testing.T) {
	s := newStore(t, time.Now())
	l, _ := s.Create("alice", "homes/alice/x", "", time.Hour)

	if err := s.Revoke("bob", l.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob revoking alice's link: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Resolve(l.Token, ""); err != nil {
		t.Error("the link should still work after a failed revoke")
	}
	if err := s.Revoke("alice", l.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(l.Token, ""); !errors.Is(err, ErrNotFound) {
		t.Error("the link should be gone")
	}
}

// A link is opened as its creator, so losing access closes it — but a disabled
// account keeps working. The cap bounds that instead of pretending otherwise.
func TestLifetimeIsCapped(t *testing.T) {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	s := newStore(t, at)

	l, err := s.Create("alice", "homes/alice/x", "", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := at.Add(MaxLifetime); !l.Expires.Equal(want) {
		t.Errorf("expires = %v, want the cap %v", l.Expires, want)
	}

	// Asking for nothing gets the default, not the cap.
	l, err = s.Create("alice", "homes/alice/y", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := at.Add(DefaultLifetime); !l.Expires.Equal(want) {
		t.Errorf("expires = %v, want the default %v", l.Expires, want)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	s := newStore(t, at)

	var saved []byte
	s.Save = func([]Link) error {
		var err error
		saved, err = s.Marshal()
		return err
	}

	live, _ := s.Create("alice", "homes/alice/live", "pw", time.Hour)
	short, _ := s.Create("bob", "homes/bob/short", "", time.Minute)
	if saved == nil {
		t.Fatal("creating a link should have persisted the store")
	}

	// Reload an hour later: the short one is gone, the long one still works with
	// its password. Links surviving a restart is the point of persisting at all.
	restored := newStore(t, at.Add(30*time.Minute))
	if err := restored.Load(saved); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Resolve(live.Token, "pw"); err != nil {
		t.Errorf("the surviving link should resolve after a reload: %v", err)
	}
	if _, err := restored.Resolve(short.Token, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired link must not come back from disk: %v", err)
	}
	if restored.Len() != 1 {
		t.Errorf("Len = %d, want 1", restored.Len())
	}
}

func TestLoadTolerates(t *testing.T) {
	s := NewStore()
	for name, data := range map[string][]byte{
		"empty": nil,
		"array": []byte(`[]`),
	} {
		if err := s.Load(data); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.Load([]byte(`not json`)); err == nil {
		t.Error("malformed state must be an error rather than silently discarded")
	}
}

func TestSweep(t *testing.T) {
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	s := newStore(t, at)
	saves := 0
	s.Save = func([]Link) error { saves++; return nil }

	s.Create("alice", "homes/alice/a", "", time.Minute)
	s.Create("alice", "homes/alice/b", "", time.Hour)
	before := saves

	s.Now = func() time.Time { return at.Add(30 * time.Minute) }
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
	if saves != before+1 {
		t.Errorf("a sweep that removed something should persist once, got %d saves", saves-before)
	}

	// A sweep with nothing to do must not rewrite the file.
	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}
	if saves != before+1 {
		t.Error("an empty sweep should not persist")
	}
}

func TestTokensAreUnguessable(t *testing.T) {
	s := newStore(t, time.Now())
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		l, err := s.Create("alice", "homes/alice/x", "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if seen[l.Token] {
			t.Fatal("duplicate token")
		}
		seen[l.Token] = true
		// 24 random bytes: the URL is the credential, so it has to be far beyond
		// guessing at any rate an attacker could sustain.
		if len(l.Token) < 32 {
			t.Fatalf("token %q is too short to be the whole credential", l.Token)
		}
	}
}

func TestFileStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "shares.json")

	// A missing file is a first run, not a failure.
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	l, err := s.Create("alice", "homes/alice/x", "pw", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the store should have been written: %v", err)
	}
	// The file holds tokens, and a token is the entire credential.
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", fi.Mode().Perm())
	}

	again, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Resolve(l.Token, "pw"); err != nil {
		t.Errorf("a link must survive a restart: %v", err)
	}

	// Silently starting empty would revoke every live link without saying so.
	if err := os.WriteFile(path, []byte("{ truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Error("a malformed store must fail loudly rather than start empty")
	}
}
