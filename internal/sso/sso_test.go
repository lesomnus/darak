package sso

import (
	"strings"
	"testing"
)

func config() Config {
	return Config{
		Issuer:      "https://login.microsoftonline.com/tenant-id/v2.0",
		ClientID:    "client",
		RedirectURL: "https://darak.example.com/api/sso/callback",
	}
}

// The check that makes an address mean anything. A /common issuer verifies
// tokens from every directory in the world, each asserting whatever address its
// own tenant put in it.
func TestNewRefusesAMultiTenantIssuerWithNoTenant(t *testing.T) {
	for _, iss := range []string{
		"https://login.microsoftonline.com/common/v2.0",
		"https://login.microsoftonline.com/organizations/v2.0",
		"https://login.microsoftonline.com/consumers/v2.0",
	} {
		cfg := config()
		cfg.Issuer = iss
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%q) succeeded with no tenant pinned", iss)
		}

		cfg.Tenant = "tenant-id"
		if _, err := New(cfg); err != nil {
			t.Errorf("New(%q) with a tenant: %v", iss, err)
		}
	}
}

// A single-tenant issuer already pins the directory — the issuer check does it —
// so requiring the claim there would be a second copy of the same fact.
func TestNewAcceptsASingleTenantIssuerWithoutTenant(t *testing.T) {
	if _, err := New(config()); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestNewRefusesARelativeRedirect(t *testing.T) {
	cfg := config()
	cfg.RedirectURL = "/api/sso/callback"
	if _, err := New(cfg); err == nil {
		t.Fatal("a relative redirect URL was accepted")
	}
}

// upn and preferred_username name the directory account; email is the mailbox
// attached to it and is the most mutable of the three.
func TestAcceptKeepsClaimPriorityAndDropsNonAddresses(t *testing.T) {
	p, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	got := p.accept("Alice.Kim@Example.com", "alice.kim@example.com", "shortname", "a@example.com")
	want := []string{"alice.kim@example.com", "a@example.com"}
	if len(got) != len(want) {
		t.Fatalf("accept() = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("accept()[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

func TestAcceptFiltersByDomain(t *testing.T) {
	cfg := config()
	cfg.Domains = []string{"@Example.com ", ""}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := p.accept("alice@example.com", "alice@other.example", "bob@example.com")
	if len(got) != 2 || got[0] != "alice@example.com" || got[1] != "bob@example.com" {
		t.Errorf("accept() = %v; want only the accepted domain", got)
	}
}

// A flow is usable exactly once, so a replayed callback URL has nothing to
// match against.
func TestFlowIsSingleUse(t *testing.T) {
	f := NewFlows()
	id, fl, err := f.Begin("/some/path")
	if err != nil {
		t.Fatal(err)
	}
	if fl.State == "" || fl.Nonce == "" || fl.PKCE == "" {
		t.Fatal("a flow was begun with something missing")
	}

	got, ok := f.Take(id)
	if !ok || got.State != fl.State {
		t.Fatalf("Take() = %+v, %v", got, ok)
	}
	if _, ok := f.Take(id); ok {
		t.Error("the same flow was consumed twice")
	}
}

// Arriving back with a session in hand is exactly when an open redirect is
// worth the most.
func TestBeginRefusesAnOffSiteReturn(t *testing.T) {
	f := NewFlows()
	for _, in := range []string{"https://evil.example", "//evil.example", "javascript:alert(1)"} {
		_, fl, err := f.Begin(in)
		if err != nil {
			t.Fatal(err)
		}
		if fl.Return != "/" {
			t.Errorf("Begin(%q).Return = %q; want /", in, fl.Return)
		}
	}
	_, fl, err := f.Begin("/browse/team-a")
	if err != nil {
		t.Fatal(err)
	}
	if fl.Return != "/browse/team-a" {
		t.Errorf("Return = %q; want the path kept", fl.Return)
	}
}

// Every sign-in against a down provider must not become another outbound
// request: a room full of people retrying would be this server making the
// outage worse.
func TestDiscoveryIsNotRetriedOnEveryAttempt(t *testing.T) {
	cfg := config()
	cfg.Issuer = "https://127.0.0.1:1/nowhere"
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Warm(t.Context()); err == nil {
		t.Fatal("discovery against a dead address succeeded")
	}
	// The second attempt must be answered from the cached failure rather than by
	// dialling again, and must still say it was the provider.
	_, _, err = p.ready(t.Context())
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("second attempt: err = %v; want the cached unavailability", err)
	}
}
