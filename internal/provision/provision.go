package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/darak/internal/auth"
)

// Kind is what an endpoint answered.
type Kind string

const (
	// Created: the account was made and will appear shortly. The only outcome
	// that leads to a session without an administrator.
	Created Kind = "created"
	// Existing: the endpoint named an account that already exists. NOT
	// auto-bound — see Provisioner.Run.
	Existing Kind = "existing"
	// Accepted: understood, but not done yet. The usual shape of an endpoint
	// that opens a pull request against roster.yaml, which is what a GitOps
	// deployment would honestly do.
	Accepted Kind = "accepted"
	// Denied: this person should not have an account here.
	Denied Kind = "denied"
	// Unavailable: the endpoint could not be asked, or answered something this
	// does not understand. Never treated as permission.
	Unavailable Kind = "unavailable"
	// NoRule: nothing in the configuration applies to this identity.
	NoRule Kind = "no-rule"
)

// Outcome is one provisioning attempt.
type Outcome struct {
	Kind Kind
	// Account is the name the endpoint gave, for Created and Existing.
	Account string
	// Rule is which rule fired, for the log and the operator page.
	Rule string
	// Err carries the reason for Unavailable.
	Err error
}

// Request is the body POSTed to an endpoint.
//
// The minimum an endpoint needs to decide and to create: who the provider says
// this is, and what it calls them. Not the token, and not the rest of the
// claims — a person here may never become a user of this system, and the less
// of them that leaves this process the less there is to account for later.
type Request struct {
	Issuer  string   `json:"issuer"`
	Subject string   `json:"subject"`
	Emails  []string `json:"emails"`
	Name    string   `json:"name,omitempty"`
}

// Response is what an endpoint may answer with.
type Response struct {
	// Account is the name that was created, or that already exists. Required for
	// 200 and 201: without it this server would be back to guessing which new
	// account belongs to which address, which is the guess this whole design
	// exists to remove.
	Account string `json:"account"`
	// Reason is optional, for the log and the operator page.
	Reason string `json:"reason,omitempty"`
}

// Resolver reports whether a name is an account the system knows. It is
// satisfied by auth.Gate.
type Resolver interface {
	Exists(ctx context.Context, user string) bool
	MaySignIn(ctx context.Context, user string) (auth.Verdict, error)
}

// Provisioner runs the rules.
type Provisioner struct {
	// Config returns the configuration in force right now. It is a function
	// rather than a value because the file is reloaded while the server runs;
	// see Watcher.
	Config func() *Config

	// Gate answers whether an account exists and may sign in. The same gate the
	// sign-in path uses — this adds no new authority, it only waits for the
	// existing one to say yes.
	Gate Resolver

	// Client talks to endpoints. Nil means a client built by newClient.
	Client *http.Client

	// Secret reads a bearer token file. Nil means readSecret.
	Secret func(path string) (string, error)

	// Now is the clock, for tests.
	Now func() time.Time

	// fired remembers which identities have already caused a call, so a person
	// refreshing the page does not become an outbound request per refresh. It is
	// in memory and lost on restart, which is the right side to err on: the cost
	// of forgetting is one extra call, and the cost of remembering wrongly is a
	// person who can never be provisioned.
	mu    sync.Mutex
	fired map[string]time.Time
	calls []time.Time
}

// RefireAfter is how long before the same identity may cause another call.
// Long enough that a frustrated person clicking repeatedly costs one request;
// short enough that a genuine retry after an endpoint outage is possible
// without restarting the server.
const RefireAfter = 10 * time.Minute

// MaxCallsPerHour bounds the whole feature.
//
// Everyone the tenant will authenticate can trigger a call without having an
// account here, so this is the ceiling on what that population can aim at the
// endpoint. Reaching it is logged rather than silent: it means either an
// unusual day of onboarding or something worth looking at.
const MaxCallsPerHour = 60

func (p *Provisioner) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

// Run asks the configured endpoint to provision an identity and reports what
// happened. It does NOT bind anything; the caller decides what an outcome
// means, and only Created leads to a session.
func (p *Provisioner) Run(ctx context.Context, req Request, claims map[string]any) Outcome {
	cfg := p.Config()
	rule := cfg.Rule(req.Emails, claims)
	if rule == nil {
		return Outcome{Kind: NoRule}
	}
	name := rule.Name
	if name == "" {
		name = rule.URL
	}

	if err := p.reserve(req.Issuer + "\x00" + req.Subject); err != nil {
		return Outcome{Kind: Unavailable, Rule: name, Err: err}
	}

	res, err := p.call(ctx, cfg, rule, req)
	if err != nil {
		return Outcome{Kind: Unavailable, Rule: name, Err: err}
	}
	res.Rule = name

	// The invariant that bounds a broken endpoint: a name that ALREADY resolves
	// was not created by this call, whatever the status code said, and is
	// therefore never bound without a person looking at it.
	//
	// There is a race in principle — an endpoint that both created the account
	// and got it applied before answering would be reported as Existing. In
	// practice the reconcile has a settle delay measured in seconds, and the
	// misreading errs toward asking a human, which is the direction to be wrong
	// in.
	if res.Kind == Created && p.Gate.Exists(ctx, res.Account) {
		res.Kind = Existing
	}
	return res
}

// reserve enforces the two rate limits.
func (p *Provisioner) reserve(key string) error {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fired == nil {
		p.fired = map[string]time.Time{}
	}
	if last, ok := p.fired[key]; ok && now.Sub(last) < RefireAfter {
		return fmt.Errorf("provision: already attempted for this identity %s ago",
			now.Sub(last).Round(time.Second))
	}

	cutoff := now.Add(-time.Hour)
	kept := p.calls[:0]
	for _, t := range p.calls {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	p.calls = kept
	if len(p.calls) >= MaxCallsPerHour {
		return fmt.Errorf("provision: %d calls in the last hour, refusing more", len(p.calls))
	}

	// Recorded before the call, not after: a hanging endpoint must not leave the
	// door open for another attempt.
	p.fired[key] = now
	p.calls = append(p.calls, now)

	// Bound the memory the same way the queue is bounded — this map is keyed by
	// something an unauthenticated caller supplies.
	if len(p.fired) > 4096 {
		for k, t := range p.fired {
			if now.Sub(t) >= RefireAfter {
				delete(p.fired, k)
			}
		}
	}
	return nil
}

func (p *Provisioner) call(ctx context.Context, cfg *Config, rule *Rule, req Request) (Outcome, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Outcome{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Timeout))
	defer cancel()

	r, err := http.NewRequestWithContext(ctx, http.MethodPost, rule.URL, bytes.NewReader(body))
	if err != nil {
		return Outcome{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if rule.Auth.BearerFile != "" {
		read := p.Secret
		if read == nil {
			read = readSecret
		}
		token, err := read(rule.Auth.BearerFile)
		if err != nil {
			return Outcome{}, err
		}
		r.Header.Set("Authorization", "Bearer "+token)
	}

	client := p.Client
	if client == nil {
		client = newClient()
	}
	resp, err := client.Do(r)
	if err != nil {
		return Outcome{}, err
	}
	defer resp.Body.Close()
	// Bounded: this is somebody else's server and a response is a few fields.
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	var answer Response
	if len(bytes.TrimSpace(payload)) > 0 {
		// A body that will not parse is not fatal for the codes that do not need
		// one; the account check below is what enforces it where it matters.
		_ = json.Unmarshal(payload, &answer)
	}
	answer.Account = strings.TrimSpace(answer.Account)

	switch resp.StatusCode {
	case http.StatusCreated:
		if !auth.ValidName(answer.Account) {
			return Outcome{}, fmt.Errorf("provision: 201 without a usable account name (got %q)", answer.Account)
		}
		return Outcome{Kind: Created, Account: answer.Account}, nil
	case http.StatusOK:
		if !auth.ValidName(answer.Account) {
			return Outcome{}, fmt.Errorf("provision: 200 without a usable account name (got %q)", answer.Account)
		}
		return Outcome{Kind: Existing, Account: answer.Account}, nil
	case http.StatusAccepted:
		return Outcome{Kind: Accepted}, nil
	case http.StatusForbidden, http.StatusNoContent, http.StatusNotFound:
		return Outcome{Kind: Denied}, nil
	default:
		return Outcome{}, fmt.Errorf("provision: endpoint answered %d", resp.StatusCode)
	}
}

// Await waits for a just-created account to become usable.
//
// What it waits for is the gate — NSS resolving the name and tdbsam having it
// enabled — because that is what every sign-in asks anyway. The roster file
// landing is not the event that matters; the system agreeing with it is, and
// the deployment's reconcile loop is what gets it there.
//
// Timing out is reported, not fatal. The caller records the request as
// provisioning and the next sign-in finds the account already there.
func (p *Provisioner) Await(ctx context.Context, account string) bool {
	cfg := p.Config()
	deadline := p.now().Add(time.Duration(cfg.Wait))
	for {
		if v, err := p.Gate.MaySignIn(ctx, account); err == nil && v.Allowed {
			return true
		}
		if !p.now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// newClient is the HTTP client used for endpoints.
//
// Redirects are refused rather than followed. A followed redirect would carry
// the Authorization header — this server's credential with the endpoint — to
// whatever host the response named, which is a credential leak the endpoint
// owner could cause by accident.
func newClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("provision: refusing to follow a redirect")
		},
	}
}
