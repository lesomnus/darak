package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/auth"
)

// fakeGate answers about a set of accounts, and can gain one part-way through a
// test the way a reconcile would.
type fakeGate struct {
	mu       sync.Mutex
	accounts map[string]bool // name -> enabled
}

func (g *fakeGate) Exists(_ context.Context, user string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.accounts[user]
	return ok
}

func (g *fakeGate) MaySignIn(_ context.Context, user string) (auth.Verdict, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	enabled, ok := g.accounts[user]
	if !ok {
		return auth.Verdict{Reason: "no such account on this server"}, nil
	}
	if !enabled {
		return auth.Verdict{Reason: "account is suspended"}, nil
	}
	return auth.Verdict{Allowed: true}, nil
}

func (g *fakeGate) add(name string, enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accounts[name] = enabled
}

func endpoint(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func provisioner(t *testing.T, url string, gate *fakeGate) *Provisioner {
	t.Helper()
	cfg, err := Parse([]byte("timeout: 2s\nwait: 2s\nrules:\n  - url: " + url + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return &Provisioner{
		Config: func() *Config { return cfg },
		Gate:   gate,
		Client: &http.Client{},
	}
}

func request() Request {
	return Request{Issuer: "https://idp", Subject: "obj-1", Emails: []string{"new@example.com"}}
}

func TestCreatedIsTheOnlyOutcomeThatCanLeadToAccess(t *testing.T) {
	gate := &fakeGate{accounts: map[string]bool{}}
	srv := endpoint(t, http.StatusCreated, Response{Account: "new.person"})

	got := provisioner(t, srv.URL, gate).Run(context.Background(), request(), nil)
	if got.Kind != Created || got.Account != "new.person" {
		t.Fatalf("Run() = %+v; want created new.person", got)
	}
}

// The invariant that bounds a broken endpoint. A hook naming an account that
// already resolves did not create it, whatever it claims, and must never be
// auto-bound — that is how somebody would inherit another person's home.
func TestAnAccountThatAlreadyExistsIsNeverReportedAsCreated(t *testing.T) {
	gate := &fakeGate{accounts: map[string]bool{"admin.kim": true}}
	srv := endpoint(t, http.StatusCreated, Response{Account: "admin.kim"})

	got := provisioner(t, srv.URL, gate).Run(context.Background(), request(), nil)
	if got.Kind != Existing {
		t.Fatalf("Run() = %+v; want existing, so it goes to a human", got)
	}
}

// Even a suspended account exists. Folding "exists" into "may sign in" would
// let a disabled account be re-provisioned to whoever asked for one.
func TestASuspendedAccountStillCountsAsExisting(t *testing.T) {
	gate := &fakeGate{accounts: map[string]bool{"erin": false}}
	srv := endpoint(t, http.StatusCreated, Response{Account: "erin"})

	got := provisioner(t, srv.URL, gate).Run(context.Background(), request(), nil)
	if got.Kind != Existing {
		t.Fatalf("Run() = %+v; want existing", got)
	}
}

func TestResponseContract(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		body   any
		want   Kind
	}{
		"accepted for later":  {status: http.StatusAccepted, want: Accepted},
		"declined":            {status: http.StatusForbidden, want: Denied},
		"declined by absence": {status: http.StatusNotFound, want: Denied},
		"endpoint broke":      {status: http.StatusInternalServerError, want: Unavailable},
		// 201 with nothing to bind to is not usable: without a name this would be
		// back to guessing which new account belongs to which address.
		"created without a name": {status: http.StatusCreated, body: Response{}, want: Unavailable},
		// And a name that could never be an account is refused before it reaches
		// anything that would exec it.
		"created with an impossible name": {
			status: http.StatusCreated, body: Response{Account: "-rf"}, want: Unavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			gate := &fakeGate{accounts: map[string]bool{}}
			srv := endpoint(t, tt.status, tt.body)
			got := provisioner(t, srv.URL, gate).Run(context.Background(), request(), nil)
			if got.Kind != tt.want {
				t.Errorf("Run() = %+v; want %s", got, tt.want)
			}
		})
	}
}

// Somebody refreshing a failed sign-in must not become one outbound request per
// refresh.
func TestOneCallPerIdentity(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	p := provisioner(t, srv.URL, &fakeGate{accounts: map[string]bool{}})
	for range 5 {
		p.Run(context.Background(), request(), nil)
	}
	if calls != 1 {
		t.Errorf("endpoint was called %d times; want 1", calls)
	}

	// A different person still gets through.
	other := request()
	other.Subject = "obj-2"
	p.Run(context.Background(), other, nil)
	if calls != 2 {
		t.Errorf("a second identity was blocked by the first; calls = %d", calls)
	}
}

// The whole feature has a ceiling, because the population that can trigger it
// is everyone the tenant will authenticate.
func TestGlobalCeiling(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	p := provisioner(t, srv.URL, &fakeGate{accounts: map[string]bool{}})
	for i := range MaxCallsPerHour + 10 {
		req := request()
		req.Subject = "obj-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		p.Run(context.Background(), req, nil)
	}
	if calls > MaxCallsPerHour {
		t.Errorf("endpoint was called %d times; the ceiling is %d", calls, MaxCallsPerHour)
	}
}

// The Authorization header is this server's credential with the endpoint. A
// followed redirect would hand it to whatever host the response named.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	p := provisioner(t, srv.URL, &fakeGate{accounts: map[string]bool{}})
	p.Client = newClient()
	got := p.Run(context.Background(), request(), nil)

	if reached {
		t.Error("the redirect was followed, carrying the Authorization header with it")
	}
	if got.Kind != Unavailable {
		t.Errorf("Run() = %+v; want unavailable", got)
	}
}

func TestBearerTokenIsSentAndReadFromAFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg, err := Parse([]byte("rules:\n  - url: " + srv.URL + "\n    auth:\n      bearer_file: " + tokenFile + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Provisioner{
		Config: func() *Config { return cfg },
		Gate:   &fakeGate{accounts: map[string]bool{}},
		Client: &http.Client{},
	}
	p.Run(context.Background(), request(), nil)

	// Trailing newline trimmed: `echo token > file` is how this file gets
	// written, and the newline would otherwise fail at the endpoint.
	if seen != "Bearer s3cret" {
		t.Errorf("Authorization = %q; want the trimmed token", seen)
	}
}

// Waiting is for the reconcile to catch up, and timing out is not a failure —
// the next sign-in finds the account.
func TestAwaitReturnsWhenTheAccountAppears(t *testing.T) {
	gate := &fakeGate{accounts: map[string]bool{}}
	p := provisioner(t, "https://example.com", gate)

	go func() {
		time.Sleep(50 * time.Millisecond)
		gate.add("new.person", true)
	}()
	if !p.Await(context.Background(), "new.person") {
		t.Error("Await gave up on an account that appeared")
	}
}

func TestAwaitGivesUpWithoutFailing(t *testing.T) {
	cfg, err := Parse([]byte("wait: 10ms\nrules: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Provisioner{
		Config: func() *Config { return cfg },
		Gate:   &fakeGate{accounts: map[string]bool{}},
	}
	if p.Await(context.Background(), "never.arrives") {
		t.Error("Await claimed an account that does not exist")
	}
}

func TestNoRuleMeansTheQueue(t *testing.T) {
	cfg, err := Parse([]byte("rules:\n  - url: https://example.com\n    match:\n      domains: [other.example]\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Provisioner{
		Config: func() *Config { return cfg },
		Gate:   &fakeGate{accounts: map[string]bool{}},
	}
	if got := p.Run(context.Background(), request(), nil); got.Kind != NoRule {
		t.Errorf("Run() = %+v; want no-rule", got)
	}
}
