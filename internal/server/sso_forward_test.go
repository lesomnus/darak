package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lesomnus/darak/internal/sso"
)

// In forward-auth mode the login button (which only knows /api/sso/login) leads
// to the proxy-guarded /api/sso/forward, carrying the return target as `rd`.
func TestForwardAuthLoginRedirectsToForward(t *testing.T) {
	s := ssoServer(t)
	s.cfg.SSO = &sso.Provider{}
	s.cfg.SSOForwardAuth = true

	r := httptest.NewRequest(http.MethodGet, "/api/sso/login?return=%2Ffiles", nil)
	w := httptest.NewRecorder()
	s.handleSSOLogin(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	if got, want := w.Header().Get("Location"), "/api/sso/forward?rd=%2Ffiles"; got != want {
		t.Fatalf("Location: want %q, got %q", want, got)
	}
}

// The route only exists in forward-auth mode; otherwise it is a 404, the same
// answer a request gets for any path the deployment does not serve.
func TestForwardIs404WhenNotEnabled(t *testing.T) {
	s := ssoServer(t)
	s.cfg.SSO = &sso.Provider{} // configured, but code flow — not forward-auth

	r := httptest.NewRequest(http.MethodGet, "/api/sso/forward", nil)
	w := httptest.NewRecorder()
	s.handleSSOForward(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 when forward-auth is off, got %d", w.Code)
	}
}

// A request with no bearer never reaches verification and issues no session:
// the route is meant to sit behind the proxy, which always supplies one.
func TestForwardWithoutBearerIssuesNoSession(t *testing.T) {
	s := ssoServer(t)
	s.cfg.SSO = &sso.Provider{}
	s.cfg.SSOForwardAuth = true

	r := httptest.NewRequest(http.MethodGet, "/api/sso/forward", nil)
	w := httptest.NewRecorder()
	s.handleSSOForward(w, r)

	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName && c.Value != "" {
			t.Fatal("a session was issued for a forward-auth request with no token")
		}
	}
}

func TestBearerToken(t *testing.T) {
	for in, want := range map[string]string{
		"Bearer abc":    "abc",
		"bearer abc":    "abc", // scheme is case-insensitive
		"Bearer  abc  ": "abc", // trimmed
		"abc":           "",    // no scheme
		"Basic abc":     "",    // wrong scheme
		"Bearer":        "",
		"":              "",
	} {
		if got := bearerToken(in); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalReturn(t *testing.T) {
	for in, want := range map[string]string{
		"/files":              "/files",
		"/files?q=1":          "/files?q=1",
		"":                    "/",
		"https://evil/x":      "/", // absolute URL
		"//evil/x":            "/", // protocol-relative
		"javascript:alert(1)": "/",
	} {
		if got := localReturn(in); got != want {
			t.Errorf("localReturn(%q) = %q, want %q", in, got, want)
		}
	}
}
