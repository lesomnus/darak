// Package sso signs a person in through an OpenID Connect provider and reports
// which identity the provider asserted.
//
// It answers exactly one question — who is this — and nothing about what they
// may do. The account that identity belongs to is decided by internal/identity,
// and whether that account may open a session at all is decided by the gate in
// internal/auth, which asks the same tdbsam the password path asks. That split
// is what keeps ADR-2's single credential store true while adding a second way
// in: offboarding is still one line in roster.yaml, because the provider is
// never asked whether somebody still works here.
//
// # What is verified, and why each part matters
//
// The library checks the signature, the issuer, the audience and the expiry.
// This package adds the three that are specific to using an address as an
// identifier:
//
//   - The nonce, tied to the browser that started the flow, so a token obtained
//     elsewhere cannot be replayed into somebody else's session.
//   - The tenant. Without it, a token from any other Microsoft tenant — or a
//     personal account — verifies perfectly well and asserts whatever address
//     its holder put in their own directory. This is the check that makes an
//     address meaningful at all, and it is why a multi-tenant issuer URL is
//     refused unless a tenant is named.
//   - The domain allow-list, a second and weaker net for the case where the
//     tenant hosts addresses that are not this organisation's.
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/lesomnus/darak/internal/identity"
)

var (
	// ErrUnavailable means the provider could not be reached or its metadata
	// could not be read. Distinct from a rejected token for the same reason
	// auth.ErrUnavailable is: a broken IdP reported as "you are not allowed"
	// sends everyone to the wrong conclusion at once.
	ErrUnavailable = errors.New("sso: identity provider unavailable")
	// ErrRejected means the provider's answer did not verify.
	ErrRejected = errors.New("sso: the sign-in could not be verified")
	// ErrWrongTenant means the token came from outside the configured tenant.
	ErrWrongTenant = errors.New("sso: token is from another tenant")
	// ErrNoAddress means the token carried no address this deployment accepts.
	ErrNoAddress = errors.New("sso: no usable address in the token")
)

// Config wires a Provider.
type Config struct {
	// Issuer is the OIDC issuer URL, e.g.
	// https://login.microsoftonline.com/<tenant-id>/v2.0
	Issuer string

	ClientID     string
	ClientSecret string

	// RedirectURL must match what is registered with the provider, and must be
	// the address a browser actually reaches this server on.
	RedirectURL string

	// Tenant is the required `tid` claim.
	//
	// A single-tenant issuer URL already pins the tenant — the issuer check does
	// it — so this is optional there. It is REQUIRED for the multi-tenant
	// endpoints (/common, /organizations, /consumers), where the issuer says
	// nothing about which directory the person came from. New refuses that
	// combination rather than leaving a deployment that verifies every token in
	// the world.
	Tenant string

	// Domains, if set, is the list of address domains this deployment will
	// accept. Addresses outside it are dropped before anything is looked up.
	Domains []string

	// Scopes are requested in addition to openid, profile and email.
	Scopes []string

	// HTTPClient talks to the provider. Nil means http.DefaultClient.
	HTTPClient *http.Client

	// Retry bounds how often discovery is re-attempted after a failure.
	Retry time.Duration
}

// multiTenantPaths are the Microsoft endpoints whose issuer identifies no
// particular directory.
var multiTenantPaths = []string{"/common/", "/organizations/", "/consumers/"}

// Provider performs the flow.
type Provider struct {
	cfg     Config
	domains []string

	mu       sync.Mutex
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	nextTry  time.Time
	lastErr  error
}

// New validates the configuration. It does NOT contact the provider: discovery
// happens on first use and is retried, so an IdP that is down at boot delays SSO
// rather than stopping the server. The password path has to keep working —
// especially then, because "log in with your password instead" is the entire
// fallback.
func New(cfg Config) (*Provider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("sso: issuer, client id and redirect URL are required")
	}
	if _, err := url.Parse(cfg.Issuer); err != nil {
		return nil, fmt.Errorf("sso: issuer: %w", err)
	}
	u, err := url.Parse(cfg.RedirectURL)
	if err != nil || !u.IsAbs() {
		return nil, fmt.Errorf("sso: redirect URL must be absolute, got %q", cfg.RedirectURL)
	}
	if cfg.Tenant == "" {
		iss := strings.TrimSuffix(cfg.Issuer, "/") + "/"
		for _, p := range multiTenantPaths {
			if strings.Contains(iss, p) {
				return nil, fmt.Errorf("sso: issuer %q serves every tenant, so a token from any "+
					"directory would verify; name the tenant to accept", cfg.Issuer)
			}
		}
	}

	domains := make([]string, 0, len(cfg.Domains))
	for _, d := range cfg.Domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "@")))
		if d != "" {
			domains = append(domains, d)
		}
	}
	if cfg.Retry <= 0 {
		cfg.Retry = 30 * time.Second
	}
	return &Provider{cfg: cfg, domains: domains}, nil
}

// Issuer reports the configured issuer, which is half of an identity's key.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// ctx returns a context carrying the configured HTTP client, which is how
// go-oidc and oauth2 are told what to dial with.
func (p *Provider) ctx(ctx context.Context) context.Context {
	if p.cfg.HTTPClient == nil {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, p.cfg.HTTPClient)
}

// ready performs discovery once, and re-attempts it no more often than Retry.
//
// The rate limit matters: without it every sign-in attempt against a down
// provider becomes another outbound request, and a room full of people retrying
// turns this server into the thing making it worse.
func (p *Provider) ready(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifier != nil {
		return p.oauth, p.verifier, nil
	}
	if now := time.Now(); now.Before(p.nextTry) {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, p.lastErr)
	}

	provider, err := oidc.NewProvider(p.ctx(ctx), p.cfg.Issuer)
	if err != nil {
		p.lastErr = err
		p.nextTry = time.Now().Add(p.cfg.Retry)
		return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	scopes := append([]string{oidc.ScopeOpenID, "profile", "email"}, p.cfg.Scopes...)
	p.oauth = &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  p.cfg.RedirectURL,
		Scopes:       scopes,
	}
	p.verifier = provider.Verifier(&oidc.Config{ClientID: p.cfg.ClientID})
	p.lastErr = nil
	return p.oauth, p.verifier, nil
}

// Warm attempts discovery once, so a misconfigured issuer is reported at
// startup instead of by the first person who clicks the button.
func (p *Provider) Warm(ctx context.Context) error {
	_, _, err := p.ready(ctx)
	return err
}

// AuthCodeURL is where the browser is sent to sign in.
func (p *Provider) AuthCodeURL(ctx context.Context, f Flow) (string, error) {
	oc, _, err := p.ready(ctx)
	if err != nil {
		return "", err
	}
	return oc.AuthCodeURL(f.State,
		oidc.Nonce(f.Nonce),
		oauth2.S256ChallengeOption(f.PKCE),
	), nil
}

// Identity is what the provider asserted.
type Identity struct {
	Issuer  string
	Subject string
	// Name is display text for the approval queue. It is never matched against
	// anything: it is user-editable in most directories.
	Name string
	// Emails are the addresses the token carried, normalised, de-duplicated,
	// filtered to the accepted domains, most authoritative first.
	Emails []string

	// Claims is the verified token, for provisioning rules to match on — a
	// department, a group, whatever this organisation gates on.
	//
	// It is held in memory for the length of one sign-in and never persisted:
	// the approval queue keeps four named fields, and this is somebody who may
	// never become a user of this system at all.
	Claims map[string]any
}

// Exchange turns the code from the callback into a verified identity.
//
// Every failure here is one of two kinds and they are kept apart: ErrUnavailable
// means the question could not be asked, anything else means the answer did not
// hold up. Collapsing them would report an unreachable IdP as a rejected person.
func (p *Provider) Exchange(ctx context.Context, code string, f Flow) (*Identity, error) {
	oc, verifier, err := p.ready(ctx)
	if err != nil {
		return nil, err
	}
	ctx = p.ctx(ctx)

	tok, err := oc.Exchange(ctx, code, oauth2.VerifierOption(f.PKCE))
	if err != nil {
		// This is the provider talking to us, not the browser, so a failure is
		// far more likely to be a misconfigured secret than a forged code.
		return nil, fmt.Errorf("%w: code exchange: %v", ErrUnavailable, err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, fmt.Errorf("%w: the provider returned no id_token", ErrRejected)
	}

	// Signature, issuer, audience and expiry.
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	// The nonce ties this token to the browser that started the flow. Without
	// it, a token minted for any other client session could be pasted into this
	// callback.
	if idToken.Nonce != f.Nonce {
		return nil, fmt.Errorf("%w: nonce does not match this browser's sign-in", ErrRejected)
	}

	var claims struct {
		Tenant string `json:"tid"`
		Name   string `json:"name"`
		UPN    string `json:"upn"`
		Pref   string `json:"preferred_username"`
		Email  string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: unreadable claims: %v", ErrRejected, err)
	}
	if p.cfg.Tenant != "" && !strings.EqualFold(claims.Tenant, p.cfg.Tenant) {
		return nil, fmt.Errorf("%w: %q", ErrWrongTenant, claims.Tenant)
	}

	// Order matters. upn and preferred_username name the directory account
	// itself; email is whatever mailbox is attached to it and is the most
	// mutable of the three. The first that maps to an account wins, so the more
	// authoritative claims are offered first.
	emails := p.accept(claims.UPN, claims.Pref, claims.Email)
	if len(emails) == 0 {
		return nil, ErrNoAddress
	}

	// Read a second time, untyped. The struct above is what this package acts
	// on; this is for rules that name a claim this package has never heard of.
	all := map[string]any{}
	if err := idToken.Claims(&all); err != nil {
		// Not fatal: everything this package needs is already parsed, and a rule
		// matching on a claim will simply not match.
		all = map[string]any{}
	}

	return &Identity{
		Issuer:  idToken.Issuer,
		Subject: idToken.Subject,
		Name:    strings.TrimSpace(claims.Name),
		Emails:  emails,
		Claims:  all,
	}, nil
}

// accept normalises the candidate claims and keeps the ones this deployment
// will consider, preserving the order they were offered in.
func (p *Provider) accept(candidates ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range candidates {
		c = identity.NormalizeEmail(c)
		// preferred_username is not always an address — for some providers it is
		// a bare username. Those are dropped rather than guessed at, because
		// gluing on a domain would invent an identifier the directory never
		// asserted.
		if c == "" || seen[c] || !identity.ValidEmail(c) {
			continue
		}
		if !p.allowed(c) {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func (p *Provider) allowed(email string) bool {
	if len(p.domains) == 0 {
		return true
	}
	_, domain, _ := strings.Cut(email, "@")
	for _, d := range p.domains {
		if domain == d {
			return true
		}
	}
	return false
}

// Flow is the per-sign-in state that must survive the round trip to the
// provider and come back matched.
//
// It is held server-side and named by an opaque cookie, rather than being put
// in the cookie itself. That keeps the PKCE verifier out of the browser, and it
// means a stale or forged cookie resolves to nothing at all instead of to
// something that has to be checked for tampering. Sessions are already kept
// this way, and are dropped by a restart for the same reason.
type Flow struct {
	State string
	Nonce string
	PKCE  string
	// Return is where to send the browser afterwards. Only ever a path on this
	// server; see Flows.Begin.
	Return string

	expires time.Time
}

// FlowTTL bounds how long a sign-in may take. Long enough for a password, an
// MFA prompt and some hesitation; short enough that an abandoned attempt is not
// still usable an hour later.
const FlowTTL = 15 * time.Minute

// Flows holds in-flight sign-ins.
type Flows struct {
	mu sync.Mutex
	m  map[string]*Flow
}

func NewFlows() *Flows { return &Flows{m: map[string]*Flow{}} }

// Begin creates a flow and returns the opaque id that names it.
//
// returnTo is sanitised here rather than at the callback: an absolute URL would
// make this server an open redirector that arrives with a freshly minted
// session, which is worth more to an attacker than an ordinary one.
func (f *Flows) Begin(returnTo string) (string, Flow, error) {
	if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		returnTo = "/"
	}
	id, err := token()
	if err != nil {
		return "", Flow{}, err
	}
	state, err := token()
	if err != nil {
		return "", Flow{}, err
	}
	nonce, err := token()
	if err != nil {
		return "", Flow{}, err
	}
	fl := Flow{
		State:   state,
		Nonce:   nonce,
		PKCE:    oauth2.GenerateVerifier(),
		Return:  returnTo,
		expires: time.Now().Add(FlowTTL),
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(time.Now())
	f.m[id] = &fl
	return id, fl, nil
}

// Take consumes a flow. A flow is usable exactly once: the callback either
// completes it or it is gone, so a replayed callback URL has nothing to match.
func (f *Flows) Take(id string) (Flow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[id]
	if !ok {
		return Flow{}, false
	}
	delete(f.m, id)
	if time.Now().After(fl.expires) {
		return Flow{}, false
	}
	return *fl, true
}

// Sweep drops abandoned sign-ins.
func (f *Flows) Sweep() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(time.Now())
}

func (f *Flows) sweep(now time.Time) {
	for id, fl := range f.m {
		if now.After(fl.expires) {
			delete(f.m, id)
		}
	}
}

// Len reports how many sign-ins are in flight.
func (f *Flows) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}

func token() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
