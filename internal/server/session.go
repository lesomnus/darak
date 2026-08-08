package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

// CookieName is the session cookie.
const CookieName = "darak_session"

type session struct {
	user      string
	expiresAt time.Time
}

// Sessions maps opaque tokens to users.
//
// It is in memory, so a restart logs everyone out. That is the right trade here:
// the alternative is a signed token the server cannot revoke, and this system's
// offboarding story is that disabling an account takes effect at once —
// `roster.yaml` sets `status: disabled` and both the web and SMB paths close.
// A token that stays valid until it expires would quietly undo that.
type Sessions struct {
	ttl time.Duration

	mu sync.Mutex
	m  map[string]session
}

func NewSessions(ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Sessions{ttl: ttl, m: map[string]session{}}
}

// Create issues a token for user.
func (s *Sessions) Create(user string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[token] = session{user: user, expiresAt: time.Now().Add(s.ttl)}
	return token, nil
}

// Lookup returns the user a token belongs to.
func (s *Sessions) Lookup(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return "", false
	}
	if time.Now().After(sess.expiresAt) {
		delete(s.m, token)
		return "", false
	}
	return sess.user, true
}

// Delete revokes a token.
func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

// Sweep drops expired sessions. Lookup already refuses them; this is only so
// they do not accumulate.
func (s *Sessions) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for token, sess := range s.m {
		if now.After(sess.expiresAt) {
			delete(s.m, token)
		}
	}
}

// Len reports how many sessions are held.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// setCookie writes the session cookie.
//
// HttpOnly keeps the token away from any script on the page, which matters
// because file names and content are rendered from user-supplied data. SameSite
// blocks a cross-site form from acting as the logged-in user. Secure is left to
// the caller: forcing it would make the thing unusable over plain HTTP on a
// developer's machine, and quietly not setting it in production is worse than
// requiring the decision.
func setCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// contextWithUser attaches the authenticated user to a request context.
func contextWithUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, userKey, user)
}
