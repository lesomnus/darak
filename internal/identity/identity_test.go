package identity

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

func approved(t *testing.T, account string, emails ...string) *Store {
	t.Helper()
	s := NewStore()
	if _, err := s.Approve(account, "https://idp", "", emails, "admin", now); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return s
}

func TestByEmailIsCaseInsensitive(t *testing.T) {
	s := approved(t, "alice", "Alice@Example.COM")

	for _, in := range []string{"alice@example.com", "ALICE@EXAMPLE.COM", " alice@example.com "} {
		if got, ok := s.ByEmail(in); !ok || got != "alice" {
			t.Errorf("ByEmail(%q) = %q, %v; want alice, true", in, got, ok)
		}
	}
}

// An address answering for two accounts is not something to render as a
// warning: whichever way it resolved, one of the two would be somebody signing
// in as another person.
func TestLoadRefusesADuplicateAddress(t *testing.T) {
	s := NewStore()
	err := s.Load([]byte(`[
		{"account":"alice","emails":["shared@example.com"]},
		{"account":"bob","emails":["shared@example.com"]}
	]`))
	if err == nil {
		t.Fatal("loaded a file where one address answers for two accounts")
	}
}

func TestLoadRefusesAnImpossibleAccountName(t *testing.T) {
	s := NewStore()
	if err := s.Load([]byte(`[{"account":"-rf","emails":["a@example.com"]}]`)); err == nil {
		t.Fatal("loaded a mapping for a name that could never be an account")
	}
}

// The point of pinning: the address is only trusted until the directory object
// behind it is known, and after that a different object presenting the same
// address is refused rather than inheriting the account.
func TestPinRefusesADifferentSubject(t *testing.T) {
	s := approved(t, "alice", "alice@example.com")

	if err := s.Pin("alice", "https://idp", "object-1", now); err != nil {
		t.Fatalf("first pin: %v", err)
	}
	// Same object again is idempotent, not a conflict: this is every sign-in
	// after the first.
	if err := s.Pin("alice", "https://idp", "object-1", now); err != nil {
		t.Fatalf("re-pin of the same subject: %v", err)
	}
	err := s.Pin("alice", "https://idp", "object-2", now)
	if !errors.Is(err, ErrSubjectPinned) {
		t.Fatalf("second subject: err = %v; want ErrSubjectPinned", err)
	}
	if got, ok := s.BySubject("https://idp", "object-1"); !ok || got != "alice" {
		t.Errorf("BySubject after the refused pin = %q, %v; want alice, true", got, ok)
	}
	if _, ok := s.BySubject("https://idp", "object-2"); ok {
		t.Error("the refused subject was recorded anyway")
	}
}

// A subject is only unique within its issuer, so a second provider's subjects
// must not resolve against the first one's.
func TestSubjectIsScopedToItsIssuer(t *testing.T) {
	s := approved(t, "alice", "alice@example.com")
	if err := s.Pin("alice", "https://idp-a", "shared-id", now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.BySubject("https://idp-b", "shared-id"); ok {
		t.Error("a subject from another issuer resolved")
	}
}

func TestAttachEmailRefusesOneThatAnswersElsewhere(t *testing.T) {
	s := approved(t, "alice", "alice@example.com")
	if _, err := s.Approve("bob", "https://idp", "", []string{"bob@example.com"}, "admin", now); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachEmail("alice", "bob@example.com", now); !errors.Is(err, ErrTaken) {
		t.Fatalf("err = %v; want ErrTaken", err)
	}
	if got, _ := s.ByEmail("bob@example.com"); got != "bob" {
		t.Errorf("bob's address now answers for %q", got)
	}
}

func TestApproveRefusesAnAddressTakenByAnother(t *testing.T) {
	s := approved(t, "alice", "alice@example.com")
	_, err := s.Approve("bob", "https://idp", "obj", []string{"alice@example.com"}, "admin", now)
	if !errors.Is(err, ErrTaken) {
		t.Fatalf("err = %v; want ErrTaken", err)
	}
	// Nothing may have been half-written: bob must not exist with a pinned
	// subject and no address.
	if _, ok := s.Get("bob"); ok {
		t.Error("the refused approval left a mapping behind")
	}
	if _, ok := s.BySubject("https://idp", "obj"); ok {
		t.Error("the refused approval pinned the subject anyway")
	}
}

func TestForgetRemovesEveryIndex(t *testing.T) {
	s := approved(t, "alice", "alice@example.com", "a@example.com")
	if err := s.Pin("alice", "https://idp", "obj", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget("alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ByEmail("alice@example.com"); ok {
		t.Error("an address still resolves after Forget")
	}
	if _, ok := s.BySubject("https://idp", "obj"); ok {
		t.Error("the subject still resolves after Forget")
	}
}

func TestCheckReportsAccountsTheRosterDoesNotHave(t *testing.T) {
	s := approved(t, "alice", "alice@example.com")
	if _, err := s.Approve("erin", "https://idp", "", []string{"erin@example.com"}, "admin", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("ghost", "https://idp", "", []string{"ghost@example.com"}, "admin", now); err != nil {
		t.Fatal(err)
	}

	got := s.Check(map[string]string{"alice": "active", "erin": "disabled"})
	want := []Problem{{Account: "erin", Code: "disabled"}, {Account: "ghost", Code: "unknown"}}
	if len(got) != len(want) {
		t.Fatalf("Check() = %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Check()[%d] = %+v; want %+v", i, got[i], want[i])
		}
	}
}

// Persistence has to survive a restart with every index intact, because the
// indexes are what the sign-in path reads.
func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "identities.json")

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("alice", "https://idp", "obj", []string{"alice@example.com"}, "admin.kim", now); err != nil {
		t.Fatal(err)
	}

	back, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := back.ByEmail("alice@example.com"); !ok || got != "alice" {
		t.Errorf("ByEmail after reload = %q, %v", got, ok)
	}
	if got, ok := back.BySubject("https://idp", "obj"); !ok || got != "alice" {
		t.Errorf("BySubject after reload = %q, %v", got, ok)
	}
	m, _ := back.Get("alice")
	if m.ApprovedBy != "admin.kim" {
		t.Errorf("ApprovedBy = %q; want admin.kim", m.ApprovedBy)
	}
}

func TestFileStoreRefusesAMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	if err := replace(path, ".x-*", []byte("{ not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("a malformed store loaded as empty, silently revoking every approval")
	}
}
