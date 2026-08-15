package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A no-session request is served as the anonymous account when one is configured,
// and refused as before when it is not. The identity still comes only from the
// session or the fixed account.
func TestAuthedOrAnon(t *testing.T) {
	seen := ""
	next := func(w http.ResponseWriter, r *http.Request) { seen = userOf(r) }

	// With an anonymous account, no session -> served as it.
	s := &Server{cfg: Config{AnonymousUser: "nobody-darak"}, sessions: NewSessions(time.Hour)}
	seen = ""
	s.authedOrAnon(next)(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/files/teams/pub", nil))
	if seen != "nobody-darak" {
		t.Fatalf("no-session request served as %q, want the anonymous account", seen)
	}

	// A valid session always wins over the anonymous fallback.
	tok, _ := s.sessions.Create("alice")
	r := httptest.NewRequest("GET", "/api/files/teams/pub", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	seen = ""
	s.authedOrAnon(next)(httptest.NewRecorder(), r)
	if seen != "alice" {
		t.Fatalf("session request served as %q, want alice", seen)
	}

	// Without an anonymous account, no session -> 401, next never runs.
	off := &Server{cfg: Config{}, sessions: NewSessions(time.Hour)}
	seen = "untouched"
	w := httptest.NewRecorder()
	off.authedOrAnon(next)(w, httptest.NewRequest("GET", "/api/files/teams/pub", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no anonymous account + no session => %d, want 401", w.Code)
	}
	if seen != "untouched" {
		t.Fatalf("handler ran without a session and without an anonymous account")
	}
}

// whoami tells the interface whether it is browsing anonymously.
func TestWhoamiReportsAnonymous(t *testing.T) {
	s := &Server{cfg: Config{AnonymousUser: "nobody-darak"}, sessions: NewSessions(time.Hour)}

	w := httptest.NewRecorder()
	s.authedOrAnon(s.handleWhoami)(w, httptest.NewRequest("GET", "/api/whoami", nil))
	var me struct {
		User      string `json:"user"`
		Anonymous bool   `json:"anonymous"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.User != "nobody-darak" || !me.Anonymous {
		t.Fatalf("anonymous whoami = %+v, want the anonymous account with anonymous=true", me)
	}

	tok, _ := s.sessions.Create("alice")
	r := httptest.NewRequest("GET", "/api/whoami", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	w = httptest.NewRecorder()
	s.authedOrAnon(s.handleWhoami)(w, r)
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me.User != "alice" || me.Anonymous {
		t.Fatalf("signed-in whoami = %+v, want alice with anonymous=false", me)
	}
}

// A new object inherits its parent folder's "other" bits: world-readable under a
// 2775 folder, world-writable under 2777, unchanged under a private 2770 — and a
// file never gains the execute bit.
func TestWidenOther(t *testing.T) {
	for _, tt := range []struct {
		name         string
		mode, parent uint32
		dir          bool
		want         uint32
	}{
		{"file under read-public", 0o660, 0o2775, false, 0o664},
		{"file under write-public", 0o660, 0o2777, false, 0o666},
		{"file under private", 0o660, 0o2770, false, 0o660},
		{"dir under read-public", 0o2770, 0o2775, true, 0o2775},
		{"dir under write-public", 0o2770, 0o2777, true, 0o2777},
		{"dir under private", 0o2770, 0o2770, true, 0o2770},
		{"file never executable", 0o660, 0o2771, false, 0o660},
	} {
		if got := widenOther(tt.mode, tt.parent, tt.dir); got != tt.want {
			t.Errorf("%s: widenOther(%04o,%04o,%v) = %04o, want %04o", tt.name, tt.mode, tt.parent, tt.dir, got, tt.want)
		}
	}
}
