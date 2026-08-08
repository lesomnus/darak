package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/helper"
	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/vfs"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// recordingDoer runs against one real helper and remembers which user each
// request claimed to be for. Everything the server does to a file is decided by
// that name, so a test needs to see it.
type recordingDoer struct {
	c *helperpool.Client

	mu    sync.Mutex
	users []string
}

func (d *recordingDoer) Do(ctx context.Context, user string, req *wire.Request) (*wire.Response, *os.File, error) {
	d.mu.Lock()
	d.users = append(d.users, user)
	d.mu.Unlock()
	return d.c.Do(ctx, req)
}

func (d *recordingDoer) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.users...)
}

type fakeAuth struct {
	ok  bool
	err error
}

func (f fakeAuth) Authenticate(context.Context, string, string) (bool, error) {
	return f.ok, f.err
}

type harness struct {
	t    *testing.T
	srv  http.Handler
	root string
	doer *recordingDoer
}

func newHarness(t *testing.T, a auth.Authenticator) *harness {
	t.Helper()
	root := t.TempDir()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	helperEnd := os.NewFile(uintptr(fds[0]), "helper")
	clientEnd := os.NewFile(uintptr(fds[1]), "client")

	h, err := helper.New(root, helperEnd)
	helperEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = h.Serve() }()

	conn, err := net.FileConn(clientEnd)
	clientEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	c := helperpool.NewClient(conn.(*net.UnixConn))
	t.Cleanup(func() { c.Close(); h.Close() })

	doer := &recordingDoer{c: c}
	s, err := New(Config{
		FS:   &vfs.FS{Pool: doer},
		Auth: a,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{"homes", "homes/alice", "teams", "teams/design"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &harness{t: t, srv: s.Handler(), root: root, doer: doer}
}

// login returns a request cookie for a successful sign-in.
func (h *harness) login(user string) *http.Cookie {
	h.t.Helper()
	body := strings.NewReader(`{"user":"` + user + `","password":"pw"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	h.t.Fatal("login set no session cookie")
	return nil
}

func (h *harness) do(method, target string, body io.Reader, c *http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, target, body)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func TestUnauthenticatedIsRefused(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	for _, tt := range []struct{ method, target string }{
		{"GET", "/api/files/homes/alice"},
		{"PUT", "/api/files/homes/alice/x.txt"},
		{"DELETE", "/api/files/homes/alice/x.txt"},
		{"POST", "/api/dirs/homes/alice/sub"},
		{"GET", "/api/whoami"},
	} {
		rec := h.do(tt.method, tt.target, strings.NewReader(""), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tt.method, tt.target, rec.Code)
		}
	}
	if got := h.doer.seen(); len(got) != 0 {
		t.Errorf("the filesystem was touched without a session: %v", got)
	}
}

func TestLoginOutcomes(t *testing.T) {
	t.Run("wrong password", func(t *testing.T) {
		h := newHarness(t, fakeAuth{ok: false})
		rec := h.do("POST", "/api/login", strings.NewReader(`{"user":"alice","password":"x"}`), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", rec.Code)
		}
	})
	t.Run("store unavailable", func(t *testing.T) {
		// Not 401: answering "wrong password" when the credential store is down
		// makes every user look like they forgot theirs at once, and sends whoever
		// is on call to the wrong system.
		h := newHarness(t, fakeAuth{err: auth.ErrUnavailable})
		rec := h.do("POST", "/api/login", strings.NewReader(`{"user":"alice","password":"x"}`), nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", rec.Code)
		}
	})
	t.Run("malformed body", func(t *testing.T) {
		h := newHarness(t, fakeAuth{ok: true})
		rec := h.do("POST", "/api/login", strings.NewReader(`not json`), nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", rec.Code)
		}
	})
}

func TestSessionCookieIsHardened(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly: the page renders user-supplied names and content")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Error("cookie must be SameSite, or a cross-site form can act as the signed-in user")
	}
	if c.Value == "" {
		t.Error("empty session token")
	}
}

// Every permission decision downstream is made by running as whoever the session
// says. If any part of the request could change that name, the whole model is
// gone — so the name must come from the session and nowhere else.
func TestIdentityComesOnlyFromTheSession(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	req := httptest.NewRequest("GET", "/api/files/homes/alice?user=bob&as=bob", nil)
	req.AddCookie(c)
	req.Header.Set("X-User", "bob")
	req.Header.Set("Authorization", "Basic Ym9iOng=")
	req.SetBasicAuth("bob", "x")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	for _, u := range h.doer.seen() {
		if u != "alice" {
			t.Fatalf("the filesystem was accessed as %q; only the session's user may be used", u)
		}
	}
}

func TestListDirectory(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	if err := os.WriteFile(filepath.Join(h.root, "homes/alice/report.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.root, "homes/alice/sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A crashed upload: present on disk, and not something the user asked for.
	if err := os.WriteFile(filepath.Join(h.root, "homes/alice/.upload-abc123"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The trash IS listed — restoring from it is the point of keeping it.
	if err := os.Mkdir(filepath.Join(h.root, "homes/alice/.trash"), 0o700); err != nil {
		t.Fatal(err)
	}

	rec := h.do("GET", "/api/files/homes/alice", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Path    string  `json:"path"`
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, e := range out.Entries {
		byName[e.Name] = e
	}
	if _, ok := byName[".upload-abc123"]; ok {
		t.Error("a partial upload must not be listed; the user has no idea what it is")
	}
	if _, ok := byName[".trash"]; !ok {
		t.Error("the trash must be listed, or nothing can be restored from it")
	}
	if e := byName["report.txt"]; e.Dir || e.Size != 5 || e.Mode != "0600" {
		t.Errorf("report.txt = %#v", e)
	}
	if e := byName["sub"]; !e.Dir {
		t.Errorf("sub = %#v, want a directory", e)
	}
}

func TestDownloadSupportsRangeAndConditionalGet(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	const content = "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(h.root, "homes/alice/f.bin"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	full := h.do("GET", "/api/files/homes/alice/f.bin", nil, c)
	if full.Code != http.StatusOK || full.Body.String() != content {
		t.Fatalf("full get: %d %q", full.Code, full.Body)
	}
	etag := full.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag, so a client can never revalidate a cached copy")
	}
	if got := full.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Error("user content must not be sniffed into an executable type")
	}

	// Range: video scrubbing and resumed downloads depend on it.
	req := httptest.NewRequest("GET", "/api/files/homes/alice/f.bin", nil)
	req.AddCookie(c)
	req.Header.Set("Range", "bytes=4-7")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range get = %d, want 206", rec.Code)
	}
	if rec.Body.String() != "4567" {
		t.Errorf("range body = %q, want 4567", rec.Body)
	}

	// Conditional GET.
	req = httptest.NewRequest("GET", "/api/files/homes/alice/f.bin", nil)
	req.AddCookie(c)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("conditional get = %d, want 304", rec.Code)
	}
}

// The write protocol publishes a new inode, so a client holding the old ETag
// must not be told its copy is still current.
func TestETagChangesWhenTheFileIsReplaced(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	if rec := h.do("PUT", "/api/files/homes/alice/f.txt", strings.NewReader("v1"), c); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	first := h.do("GET", "/api/files/homes/alice/f.txt", nil, c).Header().Get("ETag")

	if rec := h.do("PUT", "/api/files/homes/alice/f.txt", strings.NewReader("v2-longer"), c); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	second := h.do("GET", "/api/files/homes/alice/f.txt", nil, c).Header().Get("ETag")

	if first == second {
		t.Errorf("ETag did not change across a replacement: %s", first)
	}
}

// A file uploaded through the web and one dropped into the SMB share have to end
// up with the same mode, or "same data, same permission rules" fails exactly
// where a user would notice: a teammate who cannot edit what you uploaded.
func TestUploadModeMatchesTheDomain(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	for p, want := range map[string]os.FileMode{
		"homes/alice/x.txt":  0o600,
		"teams/design/y.txt": 0o660,
	} {
		if rec := h.do("PUT", "/api/files/"+p, strings.NewReader("data"), c); rec.Code != http.StatusNoContent {
			t.Fatalf("put %s: %d %s", p, rec.Code, rec.Body)
		}
		fi, err := os.Stat(filepath.Join(h.root, p))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %04o, want %04o", p, fi.Mode().Perm(), want)
		}
	}
}

func TestMkdirModeMatchesTheDomain(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	if rec := h.do("POST", "/api/dirs/teams/design/sub", nil, c); rec.Code != http.StatusNoContent {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body)
	}
	fi, err := os.Stat(filepath.Join(h.root, "teams/design/sub"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Error("a team directory must be setgid, or files created inside take the creator's group")
	}
}

func TestDeleteMovesToTrash(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	if rec := h.do("PUT", "/api/files/homes/alice/x.txt", strings.NewReader("bye"), c); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Body)
	}
	if rec := h.do("DELETE", "/api/files/homes/alice/x.txt", nil, c); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(h.root, "homes/alice/x.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the file should have left its name")
	}
	ents, err := os.ReadDir(filepath.Join(h.root, "homes/alice", vfs.TrashDir))
	if err != nil || len(ents) != 1 {
		t.Fatalf("trash = %v (%v), want the deleted file", ents, err)
	}
}

func TestErrnoToStatus(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	for name, tt := range map[string]struct {
		method, target string
		body           io.Reader
		want           int
	}{
		"missing file":     {"GET", "/api/files/homes/alice/nope", nil, http.StatusNotFound},
		"escaping path":    {"GET", "/api/files/homes/alice/%2e%2e%2f%2e%2e%2fetc/shadow", nil, http.StatusNotFound},
		"outside a domain": {"PUT", "/api/files/stray.txt", strings.NewReader("x"), http.StatusBadRequest},
		"no path":          {"GET", "/api/files/", nil, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.do(tt.method, tt.target, tt.body, c)
			if rec.Code != tt.want {
				t.Errorf("got %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestLogoutInvalidatesTheSession(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")

	if rec := h.do("GET", "/api/whoami", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("whoami before logout: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/logout", nil, c); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	// Sessions are held server-side precisely so this is immediate: a signed
	// token the server could not revoke would keep working until it expired,
	// which would quietly undo the "disable the account and both paths close"
	// property the whole design leans on.
	if rec := h.do("GET", "/api/whoami", nil, c); rec.Code != http.StatusUnauthorized {
		t.Errorf("whoami after logout = %d, want 401", rec.Code)
	}
}

func TestSessionsExpireAndAreSwept(t *testing.T) {
	s := NewSessions(20 * time.Millisecond)
	token, err := s.Create("alice")
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := s.Lookup(token); !ok || u != "alice" {
		t.Fatalf("lookup = %q %v", u, ok)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := s.Lookup(token); ok {
		t.Error("an expired session must not resolve")
	}

	other, _ := s.Create("bob")
	time.Sleep(40 * time.Millisecond)
	s.Sweep()
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0 after a sweep", s.Len())
	}
	if _, ok := s.Lookup(other); ok {
		t.Error("swept session still resolves")
	}
}

func TestTokensAreUnique(t *testing.T) {
	s := NewSessions(time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tok, err := s.Create("alice")
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("duplicate session token")
		}
		seen[tok] = true
	}
}
