package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/identity"
	"github.com/lesomnus/darak/internal/provision"
	"github.com/lesomnus/darak/internal/sso"
)

// ssoServer builds just enough Server to exercise the resolution order. The
// file paths, the pool and the authenticator play no part in it: what an SSO
// sign-in decides is a NAME, and everything downstream of that is the same code
// a password sign-in reaches.
func ssoServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg:      Config{Identities: identity.NewStore(), Pending: identity.NewQueue()},
		sessions: NewSessions(time.Hour),
		flows:    sso.NewFlows(),
		notices:  newNotices(),
	}
}

func ident(subject string, emails ...string) *sso.Identity {
	return &sso.Identity{Issuer: "https://idp", Subject: subject, Name: "Somebody", Emails: emails}
}

// An approved address is trusted exactly once — to learn which directory object
// it belongs to. After that the object is what authenticates.
func TestResolveIdentityPinsOnFirstUse(t *testing.T) {
	s := ssoServer(t)
	if _, err := s.cfg.Identities.Approve("alice", "", "", []string{"alice@example.com"}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := s.resolveIdentity(context.Background(), ident("object-1", "alice@example.com"))
	if err != nil || got != "alice" {
		t.Fatalf("resolveIdentity = %q, %v; want alice", got, err)
	}
	if account, ok := s.cfg.Identities.BySubject("https://idp", "object-1"); !ok || account != "alice" {
		t.Errorf("the subject was not pinned: %q, %v", account, ok)
	}
}

// The case an address-only scheme cannot survive: somebody leaves, their
// address is reassigned a year later, and the new holder signs in. A new object
// against a pinned one resolves to nobody, and is reported.
func TestResolveIdentityRefusesAReassignedAddress(t *testing.T) {
	s := ssoServer(t)
	if _, err := s.cfg.Identities.Approve("alice", "https://idp", "object-1",
		[]string{"alice@example.com"}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := s.resolveIdentity(context.Background(), ident("object-2", "alice@example.com"))
	if err != nil {
		t.Fatalf("resolveIdentity: %v", err)
	}
	if got != "" {
		t.Fatalf("resolveIdentity = %q; want nobody", got)
	}
}

// Once the subject is pinned, the address is decoration: a rename or a domain
// change must not lock somebody out.
func TestResolveIdentityFindsAPinnedSubjectWithAnUnknownAddress(t *testing.T) {
	s := ssoServer(t)
	if _, err := s.cfg.Identities.Approve("alice", "https://idp", "object-1",
		[]string{"old@example.com"}, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := s.resolveIdentity(context.Background(), ident("object-1", "new@example.com"))
	if err != nil || got != "alice" {
		t.Fatalf("resolveIdentity = %q, %v; want alice", got, err)
	}
	// The new address is attached rather than queued: the subject already proved
	// who they are, so an approval for it would be a chore, not a decision.
	if account, ok := s.cfg.Identities.ByEmail("new@example.com"); !ok || account != "alice" {
		t.Errorf("the new address was not recorded: %q, %v", account, ok)
	}
}

func TestResolveIdentityReturnsNobodyForAnUnknownIdentity(t *testing.T) {
	s := ssoServer(t)
	got, err := s.resolveIdentity(context.Background(), ident("object-9", "stranger@example.com"))
	if err != nil || got != "" {
		t.Fatalf("resolveIdentity = %q, %v; want nobody", got, err)
	}
}

// Recording a request must grant nothing. The two stores are separate types
// with no shared lookup path precisely so this cannot become access by way of a
// missing condition.
func TestQueuedRequestGrantsNothing(t *testing.T) {
	s := ssoServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/sso/callback", nil)

	s.queueIdentity(w, r, ident("object-9", "stranger@example.com"))

	if s.cfg.Pending.Len() != 1 {
		t.Fatalf("pending = %d; want the request recorded", s.cfg.Pending.Len())
	}
	if _, ok := s.cfg.Identities.ByEmail("stranger@example.com"); ok {
		t.Error("a queued request answered as an approved mapping")
	}
	if got := w.Result().Cookies(); len(got) != 0 {
		t.Errorf("a session cookie was issued for an unapproved identity: %v", got)
	}
	if code := w.Result().StatusCode; code != http.StatusSeeOther {
		t.Errorf("status = %d; want a redirect to the notice", code)
	}
}

// The message is fetched by id rather than carried in the URL, so it is not
// text an attacker can choose and have this server render on the page that asks
// for a password.
func TestNoticeIsSingleUse(t *testing.T) {
	s := ssoServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/sso/callback", nil)
	s.notice(w, r, notice{Kind: "pending", Message: "대기 중", Address: "a@example.com"})

	loc, err := w.Result().Location()
	if err != nil {
		t.Fatal(err)
	}
	id := loc.Query().Get("sso")
	if id == "" {
		t.Fatal("no notice id in the redirect")
	}

	read := func() map[string]any {
		rw := httptest.NewRecorder()
		s.handleSSONotice(rw, httptest.NewRequest(http.MethodGet, "/api/sso/notice?id="+id, nil))
		var body map[string]any
		if err := json.NewDecoder(rw.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if got := read(); got["kind"] != "pending" || got["address"] != "a@example.com" {
		t.Fatalf("first read = %v", got)
	}
	if got := read(); got["kind"] != "" {
		t.Errorf("second read = %v; want nothing", got)
	}
}

// A deployment without a provider must look like a build without one.
func TestSSORoutesAre404WithoutAProvider(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	for _, path := range []string{"/api/sso/login", "/api/sso/callback", "/api/admin/identities"} {
		w := httptest.NewRecorder()
		h.srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound && w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d; want 404 (or 401 before the route is reached)", path, w.Code)
		}
	}
}

// --- auto-provisioning ---

// --- trust-email (auto-bind an existing account from a trusted address) ---

// accountFromEmail takes the local part only when it is already a valid account
// name, lowercased; anything else derives nothing.
func TestAccountFromEmail(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"alice@example.com", "alice", true},
		{"First.Last@example.com", "first.last", true},
		{"a+tag@example.com", "", false}, // plus-tag is not a valid name
		{"has space@example.com", "", false},
		{"@example.com", "", false},
		{"no-at-sign", "", false},
	} {
		got, ok := accountFromEmail(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("accountFromEmail(%q) = %q,%v; want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// Flag on: a trusted address whose local part names an existing account binds it
// on the spot, pinning the subject, with no approval queue.
func TestTrustEmailBindsExistingAccount(t *testing.T) {
	s := ssoServer(t)
	s.cfg.TrustEmail = true
	s.cfg.Gate = gate(map[string]bool{"alice": true})

	got, err := s.resolveIdentity(context.Background(), ident("object-1", "alice@example.com"))
	if err != nil || got != "alice" {
		t.Fatalf("resolveIdentity = %q, %v; want alice", got, err)
	}
	if account, ok := s.cfg.Identities.BySubject("https://idp", "object-1"); !ok || account != "alice" {
		t.Errorf("the subject was not pinned: %q, %v", account, ok)
	}
}

// Flag off (default): the same identity gets no shortcut — with nothing else to
// resolve it, it falls through to nobody, exactly as before.
func TestTrustEmailOffKeepsApproval(t *testing.T) {
	s := ssoServer(t)
	s.cfg.Gate = gate(map[string]bool{"alice": true}) // account exists, flag is off

	got, err := s.resolveIdentity(context.Background(), ident("object-1", "alice@example.com"))
	if err != nil || got != "" {
		t.Fatalf("resolveIdentity = %q, %v; want nobody (approval still required)", got, err)
	}
	if _, ok := s.cfg.Identities.BySubject("https://idp", "object-1"); ok {
		t.Error("nothing should have been pinned with the flag off")
	}
}

// Flag on but the derived account does not exist: no binding, falls through
// (here to nobody, since provisioning is not configured) so a genuinely new
// person still goes the normal route.
func TestTrustEmailUnknownAccountFallsThrough(t *testing.T) {
	s := ssoServer(t)
	s.cfg.TrustEmail = true
	s.cfg.Gate = gate(map[string]bool{"alice": true})

	got, err := s.resolveIdentity(context.Background(), ident("object-9", "stranger@example.com"))
	if err != nil || got != "" {
		t.Fatalf("resolveIdentity = %q, %v; want nobody", got, err)
	}
}

// Flag on, but the account the address names is already pinned to a DIFFERENT
// subject: the reassigned-address case. Refused, nothing bound.
func TestTrustEmailRefusesAReassignedAddress(t *testing.T) {
	s := ssoServer(t)
	s.cfg.TrustEmail = true
	s.cfg.Gate = gate(map[string]bool{"alice": true})
	// alice is bound to object-1 with no email on the mapping — so step 2 does
	// not catch it and the trust-email step is the one that must refuse.
	if _, err := s.cfg.Identities.Approve("alice", "https://idp", "object-1", nil, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := s.resolveIdentity(context.Background(), ident("object-2", "alice@example.com"))
	if err != nil {
		t.Fatalf("resolveIdentity: %v", err)
	}
	if got != "" {
		t.Fatalf("resolveIdentity = %q; want nobody — the address was reassigned", got)
	}
}

// gateOf answers about a set of accounts, and satisfies both the sign-in gate
// and what provisioning needs.
//
// Locked, because the real one is asked from the waiting loop while the system
// underneath it is changing — that is the whole situation being modelled here.
type gateOf struct {
	mu       sync.Mutex
	accounts map[string]bool
}

func gate(accounts map[string]bool) *gateOf {
	if accounts == nil {
		accounts = map[string]bool{}
	}
	return &gateOf{accounts: accounts}
}

func (g *gateOf) add(name string, enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accounts[name] = enabled
}

func (g *gateOf) Exists(_ context.Context, user string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.accounts[user]
	return ok
}

func (g *gateOf) MaySignIn(_ context.Context, user string) (auth.Verdict, error) {
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

// withProvisioning points a server at an endpoint that answers with status and
// body, over rules that match everything.
func withProvisioning(t *testing.T, s *Server, g *gateOf, status int, body any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(srv.Close)

	cfg, err := provision.Parse([]byte("wait: 2s\nrules:\n  - url: " + srv.URL + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Gate = g
	s.cfg.Provision = &provision.Provisioner{
		Config: func() *provision.Config { return cfg },
		Gate:   g,
		Client: &http.Client{},
	}
}

// The one path where access appears with nobody clicking anything. It has to
// end with the identity bound and the subject pinned, exactly as an approval
// would have left it.
func TestProvisioningBindsWithoutApproval(t *testing.T) {
	s := ssoServer(t)
	// The account must NOT exist at the moment the endpoint answers -- that is
	// the invariant -- and then appear, the way a reconcile would land it.
	g := gate(nil)
	withProvisioning(t, s, g, http.StatusCreated, map[string]string{"account": "new.person"})
	go func() {
		time.Sleep(30 * time.Millisecond)
		g.add("new.person", true)
	}()

	got, err := s.resolveIdentity(context.Background(), ident("obj-1", "new@example.com"))
	if err != nil || got != "new.person" {
		t.Fatalf("resolveIdentity = %q, %v; want new.person", got, err)
	}
	if account, ok := s.cfg.Identities.BySubject("https://idp", "obj-1"); !ok || account != "new.person" {
		t.Errorf("the provisioned identity was not pinned: %q, %v", account, ok)
	}
	if s.cfg.Pending.Len() != 0 {
		t.Error("a provisioned identity was also queued for approval")
	}
}

// A hook naming somebody else's existing account must not hand it over. This is
// the sharpest failure this feature could have, so it is checked here as well
// as in the package that enforces it.
func TestProvisioningNeverBindsAnExistingAccount(t *testing.T) {
	s := ssoServer(t)
	withProvisioning(t, s, gate(map[string]bool{"admin.kim": true}), http.StatusCreated,
		map[string]string{"account": "admin.kim"})

	got, err := s.resolveIdentity(context.Background(), ident("obj-9", "stranger@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("resolveIdentity = %q; want nobody — that account already existed", got)
	}
	if _, ok := s.cfg.Identities.Get("admin.kim"); ok {
		t.Error("a mapping was written for an account the endpoint did not create")
	}
}

// Everything short of "created, and now usable" falls back to the queue.
func TestProvisioningFallsBackToTheQueue(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		body   any
	}{
		"accepted for later": {status: http.StatusAccepted},
		"declined":           {status: http.StatusForbidden},
		"endpoint broke":     {status: http.StatusBadGateway},
		// Created, but the reconcile never ran: not an error, and the next
		// sign-in will find it.
		"never converged": {status: http.StatusCreated, body: map[string]string{"account": "slow.person"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := ssoServer(t)
			withProvisioning(t, s, gate(nil), tt.status, tt.body)
			// A wait long enough to notice, short enough not to slow the suite.
			cfg, err := provision.Parse([]byte("wait: 10ms\nrules:\n  - url: " +
				s.cfg.Provision.Config().Rules[0].URL + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			s.cfg.Provision.Config = func() *provision.Config { return cfg }

			got, err := s.resolveIdentity(context.Background(), ident("obj-2", "someone@example.com"))
			if err != nil || got != "" {
				t.Fatalf("resolveIdentity = %q, %v; want nobody", got, err)
			}
		})
	}
}

// A callback with no flow cookie, a stale one, or a forged state must not be
// distinguishable from each other, and none of them may produce a session.
func TestCallbackWithoutAFlowIssuesNoSession(t *testing.T) {
	s := ssoServer(t)
	s.cfg.SSO = &sso.Provider{}

	for name, cookie := range map[string]string{
		"no cookie":    "",
		"unknown flow": "not-a-flow",
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/sso/callback?code=x&state=y", nil)
			if cookie != "" {
				r.AddCookie(&http.Cookie{Name: flowCookie, Value: cookie})
			}
			w := httptest.NewRecorder()
			s.handleSSOCallback(w, r)

			for _, c := range w.Result().Cookies() {
				if c.Name == CookieName && c.Value != "" {
					t.Fatal("a session was issued for a callback with no matching flow")
				}
			}
		})
	}
}
