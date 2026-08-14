// Package identity binds what an identity provider asserts to an account this
// server has.
//
// It holds no accounts and grants no access. roster.yaml still creates people
// and pins their uids, and the sign-in gate still decides whether an account may
// open a session; what is here is only the translation between "the directory
// says this is seunghyun.hwang@example.com" and "that is the account
// seunghyun.hwang".
//
// # Why this is not in the roster
//
// The roster is version-controlled because a uid is welded to bytes on disk:
// ZFS snapshots are immutable, so a uid that was once wrong stays wrong forever
// and a reused one inherits somebody else's files. That is what earns a
// reviewed commit, a tombstone and a git history. An address has none of those
// properties — correcting one is a single edit with no trace left behind — and
// nothing on disk records it. It belongs to the same class as the SMB password:
// mutable, consequential only at sign-in, and already kept outside the ledger
// (internal/admin/ops.go says where that line is and why).
//
// # Two stores, not one list with a flag
//
// Mappings grant, Requests do not. They are separate types in separate files
// with no shared lookup path, because the difference between them IS the
// difference between having access and not having it — and a missing `if` in a
// shared path is a much smaller mistake to make than calling the wrong
// function.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/darak/internal/auth"
)

var (
	// ErrNoMapping means nothing here answers for that identity.
	ErrNoMapping = errors.New("identity: no mapping")
	// ErrTaken means the address or subject already answers for another account.
	ErrTaken = errors.New("identity: already mapped to another account")
	// ErrSubjectPinned means the account is bound to a different directory
	// object than the one presenting itself. See Pin.
	ErrSubjectPinned = errors.New("identity: account is pinned to a different subject")
	// ErrBadAccount means the name could not be a managed account.
	ErrBadAccount = errors.New("identity: not a possible account name")
	// ErrBadAddress means the address is not usable as a login identifier.
	ErrBadAddress = errors.New("identity: not a usable address")
)

// Mapping is one approved binding.
type Mapping struct {
	Account string `json:"account"`

	// Emails are the addresses that may present themselves as this account.
	// Plural because one person legitimately has several — an alias, an address
	// from before a domain change — and because which of them the provider
	// actually asserts is not something an operator can predict.
	Emails []string `json:"emails"`

	// Issuer and Subject are the provider's immutable handle for this person,
	// recorded the first time they sign in (see Pin). Once set they are what
	// authenticates: an address can be reassigned to a new hire, a subject
	// cannot. They are empty until that first sign-in, which is the only window
	// in which an address is trusted on its own.
	Issuer  string `json:"issuer,omitempty"`
	Subject string `json:"subject,omitempty"`

	ApprovedBy string    `json:"approved_by,omitempty"`
	ApprovedAt time.Time `json:"approved_at,omitzero"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
}

// Store holds the approved mappings.
//
// Persistence is a file the operator names, outside the served tree, for the
// reason nas-design.md §7 gives: the data volume becomes a shared filesystem
// with several gateways mounting it, and application state must not be sitting
// on it when that happens.
type Store struct {
	// Save persists the current set. Nil keeps everything in memory, which is
	// only useful in tests — an approval that did not survive a restart would be
	// worse than no approval at all.
	Save func(mappings []Mapping) error

	mu sync.Mutex
	// byAccount is the record. The other two are indexes built from it, and are
	// rebuilt whole rather than patched, so they cannot drift out of step with
	// what is written to disk.
	byAccount map[string]*Mapping
	byEmail   map[string]string
	bySubject map[string]string
}

func NewStore() *Store {
	s := &Store{}
	s.reset()
	return s
}

func (s *Store) reset() {
	s.byAccount = map[string]*Mapping{}
	s.byEmail = map[string]string{}
	s.bySubject = map[string]string{}
}

// subjectKey identifies a directory object. The issuer is part of it because
// `sub` is only unique within one provider, and a deployment that later adds a
// second one must not have the first one's subjects answer for it.
func subjectKey(issuer, subject string) string { return issuer + "\x00" + subject }

// NormalizeEmail lowercases and trims an address.
//
// SMTP says the local part is case-sensitive. No directory in practice treats it
// that way, and Entra will happily assert the same mailbox with different
// capitalisation depending on which claim it came from — so comparing raw would
// produce a person who can sign in on Monday and not on Tuesday.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidEmail is a deliberately shallow check: one '@', something on each side,
// no spaces or control characters.
//
// It is not trying to decide whether mail would be delivered — that is not the
// question, and a stricter rule would eventually refuse a real address somebody
// actually signs in with. It only refuses values that could not have come from
// a directory at all.
func ValidEmail(s string) bool {
	if s == "" || len(s) > 254 {
		return false
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return false
	}
	if !strings.Contains(domain, ".") {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// Load replaces the contents from JSON.
//
// A malformed or contradictory file is an error rather than an empty start. The
// share store makes the same call for the same reason: silently starting empty
// would revoke every approval without saying so, and everyone would be told
// their address is unknown at once.
func (s *Store) Load(data []byte) error {
	var list []Mapping
	if len(data) > 0 {
		if err := json.Unmarshal(data, &list); err != nil {
			return fmt.Errorf("identity: parse mappings: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
	for i := range list {
		m := list[i]
		if !auth.ValidName(m.Account) {
			return fmt.Errorf("identity: %w: %q", ErrBadAccount, m.Account)
		}
		if _, dup := s.byAccount[m.Account]; dup {
			return fmt.Errorf("identity: %q appears twice", m.Account)
		}
		emails := make([]string, 0, len(m.Emails))
		for _, e := range m.Emails {
			e = NormalizeEmail(e)
			if !ValidEmail(e) {
				return fmt.Errorf("identity: %w: %q on %q", ErrBadAddress, e, m.Account)
			}
			// An address that answers for two accounts is not a warning to render
			// on a page: whichever way it resolved would be arbitrary, and one of
			// the two would be somebody signing in as another person.
			if other, dup := s.byEmail[e]; dup {
				return fmt.Errorf("identity: %q is mapped to both %q and %q", e, other, m.Account)
			}
			s.byEmail[e] = m.Account
			emails = append(emails, e)
		}
		m.Emails = emails
		if m.Subject != "" {
			k := subjectKey(m.Issuer, m.Subject)
			if other, dup := s.bySubject[k]; dup {
				return fmt.Errorf("identity: subject %q is mapped to both %q and %q", m.Subject, other, m.Account)
			}
			s.bySubject[k] = m.Account
		}
		s.byAccount[m.Account] = &m
	}
	return nil
}

// Marshal renders the store for persistence, sorted so a diff of the file shows
// what changed rather than how the map happened to iterate.
func (s *Store) Marshal() ([]byte, error) {
	s.mu.Lock()
	list := s.list()
	s.mu.Unlock()
	return json.MarshalIndent(list, "", "  ")
}

// list returns a copy, sorted by account. Callers must hold the lock.
func (s *Store) list() []Mapping {
	out := make([]Mapping, 0, len(s.byAccount))
	for _, m := range s.byAccount {
		c := *m
		c.Emails = append([]string(nil), m.Emails...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out
}

// List returns every mapping.
func (s *Store) List() []Mapping {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

// BySubject resolves the provider's immutable handle to an account.
//
// This is the authoritative lookup once a subject has been pinned. Callers ask
// it FIRST, before any address, so that an approved person keeps working when
// their address changes and a reassigned address never resolves to whoever used
// to hold it.
func (s *Store) BySubject(issuer, subject string) (string, bool) {
	if subject == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.bySubject[subjectKey(issuer, subject)]
	return account, ok
}

// ByEmail resolves an asserted address to an account.
//
// Only meaningful before a subject is pinned — it is the bootstrap, not the
// credential. It is safe there because the caller has already refused any token
// from outside the configured tenant, and inside a tenant the directory itself
// enforces that one address belongs to one person.
func (s *Store) ByEmail(email string) (string, bool) {
	email = NormalizeEmail(email)
	if email == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.byEmail[email]
	return account, ok
}

// Get returns one mapping.
func (s *Store) Get(account string) (Mapping, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byAccount[account]
	if !ok {
		return Mapping{}, false
	}
	c := *m
	c.Emails = append([]string(nil), m.Emails...)
	return c, true
}

// Pin records the directory object that signed in as this account, the first
// time it does.
//
// Trust on first use, and the window is exactly one sign-in wide. Before it the
// account can be claimed by whoever holds one of its approved addresses; after
// it, only by that object. What this buys is the case an approved-address-only
// scheme cannot survive: someone leaves, the company reassigns their address a
// year later, and the new holder signs in — a new subject against a pinned one,
// which is refused and reported rather than silently becoming the old employee.
//
// Re-pinning is deliberately not offered as a normal operation. An operator who
// really must (the directory was rebuilt and every object id changed) removes
// the mapping and approves it again, which is one deliberate act that shows up
// in the audit trail as what it is.
func (s *Store) Pin(account, issuer, subject string, now time.Time) error {
	if subject == "" {
		return fmt.Errorf("identity: refusing to pin an empty subject on %q", account)
	}
	s.mu.Lock()
	m, ok := s.byAccount[account]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNoMapping, account)
	}
	if m.Subject != "" {
		s.mu.Unlock()
		if m.Subject == subject && m.Issuer == issuer {
			return nil
		}
		return fmt.Errorf("%w: %q", ErrSubjectPinned, account)
	}
	k := subjectKey(issuer, subject)
	if other, dup := s.bySubject[k]; dup {
		s.mu.Unlock()
		return fmt.Errorf("%w: subject already answers for %q", ErrTaken, other)
	}
	m.Issuer, m.Subject, m.UpdatedAt = issuer, subject, now
	s.bySubject[k] = account
	s.mu.Unlock()
	return s.save()
}

// AttachEmail adds an address to a mapping whose subject already matched.
//
// The person is already authenticated as this account by their subject, so the
// address they arrived with is theirs by the same evidence that let them in.
// Requiring a second approval for each alias would put a queue item in front of
// an operator that they can only rubber-stamp, and would lock the person out of
// nothing in the meantime — the subject already worked.
func (s *Store) AttachEmail(account, email string, now time.Time) error {
	email = NormalizeEmail(email)
	if !ValidEmail(email) {
		return fmt.Errorf("%w: %q", ErrBadAddress, email)
	}
	s.mu.Lock()
	m, ok := s.byAccount[account]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNoMapping, account)
	}
	if other, dup := s.byEmail[email]; dup {
		s.mu.Unlock()
		if other == account {
			return nil
		}
		return fmt.Errorf("%w: %q", ErrTaken, other)
	}
	m.Emails = append(m.Emails, email)
	sort.Strings(m.Emails)
	m.UpdatedAt = now
	s.byEmail[email] = account
	s.mu.Unlock()
	return s.save()
}

// Approve binds an identity to an account.
//
// The subject is recorded here rather than left for the first sign-in, because
// by the time an operator is approving a queued request the person has already
// presented that subject to this server. Waiting would throw away the strongest
// identifier we will ever have about them.
func (s *Store) Approve(account, issuer, subject string, emails []string, by string, now time.Time) (Mapping, error) {
	if !auth.ValidName(account) {
		return Mapping{}, fmt.Errorf("%w: %q", ErrBadAccount, account)
	}
	clean := make([]string, 0, len(emails))
	for _, e := range emails {
		e = NormalizeEmail(e)
		if !ValidEmail(e) {
			return Mapping{}, fmt.Errorf("%w: %q", ErrBadAddress, e)
		}
		clean = append(clean, e)
	}

	s.mu.Lock()
	// Every conflict is checked before anything is written: a half-applied
	// approval would leave an index disagreeing with the record.
	for _, e := range clean {
		if other, dup := s.byEmail[e]; dup && other != account {
			s.mu.Unlock()
			return Mapping{}, fmt.Errorf("%w: %q answers for %q", ErrTaken, e, other)
		}
	}
	if subject != "" {
		if other, dup := s.bySubject[subjectKey(issuer, subject)]; dup && other != account {
			s.mu.Unlock()
			return Mapping{}, fmt.Errorf("%w: subject answers for %q", ErrTaken, other)
		}
	}
	m, ok := s.byAccount[account]
	if !ok {
		m = &Mapping{Account: account, ApprovedBy: by, ApprovedAt: now}
		s.byAccount[account] = m
	}
	if subject != "" {
		if m.Subject != "" && (m.Subject != subject || m.Issuer != issuer) {
			s.mu.Unlock()
			return Mapping{}, fmt.Errorf("%w: %q", ErrSubjectPinned, account)
		}
		m.Issuer, m.Subject = issuer, subject
		s.bySubject[subjectKey(issuer, subject)] = account
	}
	for _, e := range clean {
		if _, have := s.byEmail[e]; !have {
			m.Emails = append(m.Emails, e)
			s.byEmail[e] = account
		}
	}
	sort.Strings(m.Emails)
	m.UpdatedAt = now
	if m.ApprovedBy == "" {
		m.ApprovedBy, m.ApprovedAt = by, now
	}
	out := *m
	out.Emails = append([]string(nil), m.Emails...)
	s.mu.Unlock()
	return out, s.save()
}

// Forget removes a mapping entirely.
//
// It does not close a session that mapping opened, and it is not how somebody
// is offboarded — that is `status: disabled` in the roster, which shuts SMB and
// both web paths at once. This is for a mapping that is wrong, not for a person
// who has left.
func (s *Store) Forget(account string) error {
	s.mu.Lock()
	m, ok := s.byAccount[account]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNoMapping, account)
	}
	for _, e := range m.Emails {
		delete(s.byEmail, e)
	}
	if m.Subject != "" {
		delete(s.bySubject, subjectKey(m.Issuer, m.Subject))
	}
	delete(s.byAccount, account)
	s.mu.Unlock()
	return s.save()
}

// DetachEmail removes one address, leaving the mapping in place.
func (s *Store) DetachEmail(account, email string, now time.Time) error {
	email = NormalizeEmail(email)
	s.mu.Lock()
	m, ok := s.byAccount[account]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNoMapping, account)
	}
	kept := m.Emails[:0]
	found := false
	for _, e := range m.Emails {
		if e == email {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q is not on %q", ErrNoMapping, email, account)
	}
	m.Emails = kept
	m.UpdatedAt = now
	delete(s.byEmail, email)
	s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	if s.Save == nil {
		return nil
	}
	return s.Save(s.List())
}

// Problem is a mapping that does not line up with the roster.
type Problem struct {
	Account string `json:"account"`
	// Code is "unknown" (no such entry in the roster), "disabled" or "reserved".
	Code string `json:"code"`
}

// Check compares the mappings against the declared roster.
//
// The result is a report, not an enforcement: a mapping pointing at a purged
// account grants nothing, because the gate asks NSS and tdbsam on every single
// sign-in. Refusing to start over it — which is what a hand-edited, GitOps'd
// file would have to do, since fixing it means a commit — would take the server
// down for a stale row. Here the operator can fix it on the page, so the right
// answer is to tell them, loudly, and keep serving.
func (s *Store) Check(status map[string]string) []Problem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Problem{}
	for _, account := range sortedKeys(s.byAccount) {
		st, ok := status[account]
		switch {
		case !ok:
			out = append(out, Problem{Account: account, Code: "unknown"})
		case st == "disabled" || st == "reserved":
			out = append(out, Problem{Account: account, Code: st})
		}
	}
	return out
}

func sortedKeys(m map[string]*Mapping) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
