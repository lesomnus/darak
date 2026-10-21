package uiproc

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The interface is static files; the session is the one thing in a request it
// has no use for and must never see. Nor may it set a cookie on the server's
// origin, which is the API's.
func TestProxyForwardsNoCredentials(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		http.SetCookie(w, &http.Cookie{Name: "darak_session", Value: "forged"})
		_, _ = w.Write([]byte(`<div id="root"></div>`))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "http://darak.example.com/teams/design", nil)
	req.Header.Set("Cookie", "darak_session=secret")
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	Proxy(target, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got == nil {
		t.Fatal("the request never reached the interface")
	}
	if got.URL.Path != "/teams/design" {
		t.Errorf("path = %q, want /teams/design", got.URL.Path)
	}
	if got.Host != "darak.example.com" {
		t.Errorf("Host = %q, want the browser's", got.Host)
	}
	for _, h := range []string{"Cookie", "Authorization"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("%s reached the interface: %q", h, v)
		}
	}
	if v := rec.Header().Get("Set-Cookie"); v != "" {
		t.Errorf("the interface set a cookie on the server's origin: %q", v)
	}
}

// A child that is not running is answered at once, not with a request that
// waits in a backlog nobody is accepting from.
func TestProxyNotReady(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	rec := httptest.NewRecorder()
	Proxy(target, func() bool { return false }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a 503 that will clear by itself")
	}
}

func TestProxyUpstreamDown(t *testing.T) {
	// Listening on nothing: the port is closed, so the dial fails.
	srv := httptest.NewServer(http.NotFoundHandler())
	target, _ := url.Parse(srv.URL)
	srv.Close()

	rec := httptest.NewRecorder()
	Proxy(target, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestParseUser(t *testing.T) {
	c, err := ParseUser("65534:65534")
	if err != nil {
		t.Fatal(err)
	}
	if c != (Creds{UID: 65534, GID: 65534}) {
		t.Errorf("got %+v", c)
	}

	for _, bad := range []string{"0:0", "0:65534", "65534:0", "root", "x:1", "1:x", "no-such-user-here"} {
		if _, err := ParseUser(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestStartRefusesRoot(t *testing.T) {
	for _, c := range []Creds{{0, 0}, {0, 1000}, {1000, 0}} {
		if s, err := Start(Config{Bin: "/bin/true", Dir: "/", Creds: c}); err == nil {
			s.Close()
			t.Errorf("started as %+v", c)
		}
	}
}
