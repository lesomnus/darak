// Package share issues capability links: a URL that lets whoever holds it fetch
// one file, until it expires or is revoked.
//
// This is the narrow exception to "permissions are the filesystem's" that
// nas-design.md §10 accepts. It is narrow because it changes nothing on disk:
// the server opens the file as the person who made the link, so the kernel still
// decides, and the grant covers exactly one path for a bounded time.
//
// Unlike an S3 presigned URL the token is stored rather than signed, so it can
// be revoked before it expires. That matters here: this system's story is that
// access is closed immediately — a logout ends a session at once, and disabling
// an account shuts both the web and SMB paths — and a signature the server
// cannot take back would quietly be the one thing that outlives all of it.
package share

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MaxLifetime caps how long a link can be asked to live.
//
// A link is opened as its creator, so losing access to the file closes it — but
// `status: disabled` keeps the unix account, and that user's links would go on
// working. The cap bounds that rather than pretending it cannot happen;
// offboarding should still revoke them.
const MaxLifetime = 30 * 24 * time.Hour

// DefaultLifetime is used when a request does not say.
const DefaultLifetime = 7 * 24 * time.Hour

var (
	// ErrNotFound covers an unknown, expired or revoked token. They are one error
	// on purpose: telling them apart would confirm that a token once existed.
	ErrNotFound = errors.New("share: no such link")
	// ErrPassword means the link needs a password, or the one given is wrong.
	ErrPassword = errors.New("share: password required or incorrect")
)

// Link is one issued capability.
type Link struct {
	Token string `json:"token"`
	// Owner is whose credentials the file is opened with. Every fetch runs as
	// them, so the kernel re-checks on each one.
	Owner   string    `json:"owner"`
	Path    string    `json:"path"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`

	// PasswordHash is a salted SHA-256 of the optional password, so the store
	// (which is written to disk) never holds the password itself.
	PasswordSalt []byte `json:"password_salt,omitempty"`
	PasswordHash []byte `json:"password_hash,omitempty"`
}

// Protected reports whether a password is needed.
func (l *Link) Protected() bool { return len(l.PasswordHash) > 0 }

// Expired reports whether the link has passed its expiry.
func (l *Link) Expired(now time.Time) bool { return now.After(l.Expires) }

// Store holds the issued links.
//
// Persistence is a file the operator configures, not anything in the served
// tree: nas-design.md §7 is explicit that application state must not live on the
// data volume, because that volume is the thing that later moves to a shared
// filesystem and gets mounted by more than one gateway.
type Store struct {
	// Save persists the current set. It may be nil, in which case links live only
	// as long as the process — which is a bad experience across an upgrade, so
	// the server always provides one.
	Save func(links []Link) error

	// Now is overridable for tests.
	Now func() time.Time

	mu sync.Mutex
	m  map[string]*Link
}

// NewStore builds an empty store.
func NewStore() *Store { return &Store{m: map[string]*Link{}} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Load replaces the contents from previously saved JSON. Expired links are
// dropped rather than loaded and immediately refused.
func (s *Store) Load(data []byte) error {
	var links []Link
	if len(data) > 0 {
		if err := json.Unmarshal(data, &links); err != nil {
			return fmt.Errorf("share: load: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[string]*Link, len(links))
	now := s.now()
	for i := range links {
		l := links[i]
		if l.Token == "" || l.Expired(now) {
			continue
		}
		s.m[l.Token] = &l
	}
	return nil
}

// Marshal renders the store for persistence, in a stable order.
func (s *Store) Marshal() ([]byte, error) {
	s.mu.Lock()
	links := s.snapshot()
	s.mu.Unlock()
	return json.MarshalIndent(links, "", "  ")
}

// snapshot returns the live links sorted by token. Caller holds the lock.
func (s *Store) snapshot() []Link {
	out := make([]Link, 0, len(s.m))
	now := s.now()
	for _, l := range s.m {
		if l.Expired(now) {
			continue
		}
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out
}

// Create issues a link. An empty password means the link is open to anyone
// holding the URL.
func (s *Store) Create(owner, path, password string, lifetime time.Duration) (*Link, error) {
	if owner == "" || path == "" {
		return nil, errors.New("share: owner and path are required")
	}
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	if lifetime > MaxLifetime {
		lifetime = MaxLifetime
	}

	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	now := s.now()
	l := &Link{
		Token:   base64.RawURLEncoding.EncodeToString(raw[:]),
		Owner:   owner,
		Path:    path,
		Created: now,
		Expires: now.Add(lifetime),
	}
	if password != "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		l.PasswordSalt = salt
		l.PasswordHash = hashPassword(salt, password)
	}

	s.mu.Lock()
	s.m[l.Token] = l
	links := s.snapshot()
	s.mu.Unlock()

	if err := s.persist(links); err != nil {
		return nil, err
	}
	out := *l
	return &out, nil
}

// Resolve returns the link a token names, checking expiry and password.
func (s *Store) Resolve(token, password string) (*Link, error) {
	s.mu.Lock()
	l, ok := s.m[token]
	if ok && l.Expired(s.now()) {
		delete(s.m, token)
		ok = false
	}
	if !ok {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	out := *l
	s.mu.Unlock()

	if out.Protected() {
		want := out.PasswordHash
		got := hashPassword(out.PasswordSalt, password)
		// Constant time, so the comparison does not report how much of a guess
		// was right.
		if subtle.ConstantTimeCompare(want, got) != 1 {
			return nil, ErrPassword
		}
	}
	return &out, nil
}

// Get returns a link WITHOUT checking its password.
//
// It exists for the one caller that has already established the password was
// given — the unlock cookie — and for showing the file's name on the page that
// asks for it. Everything else must go through Resolve; this is the only way
// past the password, and keeping it a separate, named method is what makes that
// visible at every call site.
func (s *Store) Get(token string) (*Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.m[token]
	if !ok || l.Expired(s.now()) {
		return nil, ErrNotFound
	}
	out := *l
	return &out, nil
}

// Revoke removes a link. Only its owner may.
func (s *Store) Revoke(owner, token string) error {
	s.mu.Lock()
	l, ok := s.m[token]
	if !ok || l.Owner != owner {
		s.mu.Unlock()
		// Same error either way: whether a token exists is not something a
		// non-owner should be able to find out by asking.
		return ErrNotFound
	}
	delete(s.m, token)
	links := s.snapshot()
	s.mu.Unlock()
	return s.persist(links)
}

// ListByOwner returns one user's live links, newest first.
func (s *Store) ListByOwner(owner string) []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Link
	now := s.now()
	for _, l := range s.m {
		if l.Owner == owner && !l.Expired(now) {
			c := *l
			// The stored hash never leaves the store; a caller only needs to know
			// that a password is set.
			c.PasswordHash, c.PasswordSalt = nil, nil
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Sweep drops expired links and persists the result.
func (s *Store) Sweep() error {
	s.mu.Lock()
	now := s.now()
	changed := false
	for token, l := range s.m {
		if l.Expired(now) {
			delete(s.m, token)
			changed = true
		}
	}
	links := s.snapshot()
	s.mu.Unlock()
	if !changed {
		return nil
	}
	return s.persist(links)
}

// Len reports how many live links are held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.snapshot())
}

func (s *Store) persist(links []Link) error {
	if s.Save == nil {
		return nil
	}
	return s.Save(links)
}

// hashPassword salts and hashes a link password.
//
// This is not a login credential: it guards one file for a bounded time, is
// chosen per link rather than reused, and the store is not reachable without
// already being root. A salted hash keeps the plaintext off disk, which is what
// it needs to do.
func hashPassword(salt []byte, password string) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	return h.Sum(nil)
}
