package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CookieName is the session cookie.
const CookieName = "darak_session"

// Sessions issues and verifies session cookies.
//
// A cookie is a SIGNED, self-contained token — `base64(user|iat|exp).base64(mac)`,
// where mac is HMAC-SHA256 over `user|iat|exp` with a key shared by every replica.
// There is no server-side session table, so any pod verifies any pod's cookie and
// a restart no longer logs everyone out — which is what lets the web tier run more
// than one pod and roll without dropping people.
//
// The trade the old in-memory table made was revocability. Two of its three uses
// are kept without the table: a sign-in is still refused for a disabled account
// (the login gate), and "log everyone else out when I change my password" is kept
// by an epoch — a per-user not-before time bumped on a password change, checked
// on every request (see epochStore). The third, killing one specific token, is
// now best effort: logout clears the cookie in the browser, but a token already
// copied out would stay valid until it expires. HttpOnly, Secure and a bounded
// TTL are what bound that; an opaque table that could be emptied bought immediate
// revocation of a stolen token, and that is the one thing given up here.
type Sessions struct {
	ttl    time.Duration
	key    []byte
	epochs *epochStore
}

// NewSessions builds the issuer. An empty key means no shared signing secret was
// configured: a random per-process key is generated, which still works — but only
// for a single replica, and a restart invalidates every cookie (the old
// behaviour). A shared key across replicas is what enables active-active.
func NewSessions(ttl time.Duration, key []byte, epochs *epochStore) *Sessions {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	return &Sessions{ttl: ttl, key: key, epochs: epochs}
}

// newToken returns an unguessable opaque identifier — 256 bits from crypto/rand.
// Used for SSO flow ids and one-off notices (not for sessions, which are signed).
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Sessions) sign(msg string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Create issues a signed token for user.
//
// The account name has no '|' (it matches firstname.lastname), so it is safe as a
// field separator; the whole message is base64'd in the token regardless.
func (s *Sessions) Create(user string) (string, error) {
	now := time.Now()
	// Milliseconds, not seconds: sub-second precision keeps the epoch comparison
	// meaningful and does not round a short TTL to zero. exp is what bounds a
	// leaked token, iat is what a password change invalidates against.
	msg := user + "|" + strconv.FormatInt(now.UnixMilli(), 10) + "|" + strconv.FormatInt(now.Add(s.ttl).UnixMilli(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(msg)) + "." + s.sign(msg), nil
}

// Lookup verifies a token and returns the user it belongs to. It rejects a bad
// signature, an expired token, and one issued before the user's session epoch (a
// password change), so a caller can trust the returned name as the session's.
func (s *Sessions) Lookup(token string) (string, bool) {
	enc, gotMAC, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	msg := string(raw)
	// Constant-time compare: a timing side channel on a MAC check is how a forged
	// token gets brute-forced one byte at a time.
	if subtle.ConstantTimeCompare([]byte(gotMAC), []byte(s.sign(msg))) != 1 {
		return "", false
	}
	user, rest, ok := strings.Cut(msg, "|")
	if !ok {
		return "", false
	}
	iatStr, expStr, ok := strings.Cut(rest, "|")
	if !ok {
		return "", false
	}
	iat, err1 := strconv.ParseInt(iatStr, 10, 64)
	exp, err2 := strconv.ParseInt(expStr, 10, 64)
	if err1 != nil || err2 != nil {
		return "", false
	}
	now := time.Now().UnixMilli()
	if now >= exp {
		return "", false
	}
	if s.epochs != nil && iat < s.epochs.notBefore(user) {
		return "", false // issued before a password change closed prior sessions
	}
	return user, true
}

// Delete is best effort for a signed cookie: there is no server-side token to
// remove, so the caller clears the browser cookie instead (clearCookie). It stays
// for the handler's shape; a stolen token is bounded by its TTL, not by this.
func (s *Sessions) Delete(token string) {}

// DeleteOthers closes every OTHER session of user by bumping their session epoch
// to now, so every cookie issued earlier stops verifying. The caller keeps their
// own session because the handler re-issues their cookie with a fresh iat after
// this returns. The count the old table could give is not knowable without one,
// so it reports whether the epoch moved, not how many tokens it invalidated.
func (s *Sessions) DeleteOthers(user, keep string) int {
	if s.epochs == nil {
		return 0
	}
	if err := s.epochs.bump(user, time.Now()); err != nil {
		return 0
	}
	return 1
}

// Sweep and Len exist for the old table's callers. A signed cookie holds no
// server state to sweep or count, so both are no-ops now.
func (s *Sessions) Sweep()   {}
func (s *Sessions) Len() int { return 0 }

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
