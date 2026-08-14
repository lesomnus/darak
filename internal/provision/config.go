// Package provision asks something outside this server to create an account
// for somebody the identity provider vouched for.
//
// It is the first thing here that can cause access to exist without a person
// acting, so what it may do is bounded in four ways, and each of them is load
// bearing:
//
//  1. **The rules are a file, never the web page.** A page that could edit them
//     would be a way to grant yourself an account; this file is a deployed,
//     reviewed artifact like the roster.
//  2. **It does not create the account.** It asks, and then waits to see the
//     account appear through NSS and tdbsam — the same two questions every
//     other sign-in answers. usersync still owns accounts and roster.yaml is
//     still the ledger, so "darak does not become a directory service"
//     (nas-design.md §2) survives intact.
//  3. **It only ever binds an identity to an account that did not already
//     exist.** See Outcome: a webhook naming an account that already resolves
//     is answered with the approval queue, not with a session. A broken hook
//     can therefore create junk accounts; it cannot hand somebody another
//     person's home directory.
//  4. **It fires once per identity**, and never in the direction of "allow" when
//     it fails.
//
// What genuinely changes is where the policy lives: it moves from an
// administrator clicking approve to whatever the endpoint decides, which is
// code in another system. The `match` block is the local net in front of that,
// and it is why domains and claims can be required here rather than trusted
// there.
package provision

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Defaults for the two durations. Both are deliberately short; see Config.
const (
	DefaultTimeout = 10 * time.Second
	DefaultWait    = 30 * time.Second
)

// Config is the whole file.
type Config struct {
	// Timeout bounds one call to an endpoint.
	Timeout Duration `yaml:"timeout,omitempty"`

	// Wait bounds how long to watch for the account to appear after a hook
	// reports it created one.
	//
	// Exceeding it is not a failure. The request is recorded as provisioning and
	// the NEXT sign-in completes it, because by then the account is simply
	// there. That is what makes this value uninteresting to tune: the worst
	// outcome of setting it too low is "press it again in a minute".
	Wait Duration `yaml:"wait,omitempty"`

	// Rules are tried in order and the FIRST match wins. An identity that
	// matches nothing goes to the approval queue exactly as it does with no
	// config file at all.
	Rules []Rule `yaml:"rules"`
}

// Rule is one endpoint and who it applies to.
//
// The json tags are for the operator page. Everything here is safe to show —
// the only secret involved is the token, and this holds the PATH to it, never
// its contents.
type Rule struct {
	// Name is for the operator page and the log. Optional.
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Match Match  `yaml:"match,omitempty" json:"match"`

	// URL receives the POST. https only, unless it is on the loopback address —
	// a sidecar on localhost is a legitimate deployment and there is no network
	// to protect there.
	URL string `yaml:"url" json:"url"`

	Auth Auth `yaml:"auth,omitempty" json:"auth"`
}

// Match narrows which identities a rule applies to.
//
// An empty Match matches everything, which is a real choice for a deployment
// where the tenant is the staff list — but it is one worth making explicitly,
// so it is not the default shape in any example.
type Match struct {
	// Domains, if set, requires one of the asserted addresses to be in one of
	// them.
	Domains []string `yaml:"domains,omitempty" json:"domains,omitempty"`

	// Claims, if set, requires each named claim to equal the given value — or,
	// when the claim is a list (a groups claim, say), to contain it.
	Claims map[string]string `yaml:"claims,omitempty" json:"claims,omitempty"`
}

// Auth is how this server proves itself to the endpoint.
type Auth struct {
	// BearerFile holds a token sent as `Authorization: Bearer …`.
	//
	// A file, for the reason the OIDC client secret is one: argv is
	// world-readable through /proc, and helpers inherit this process's
	// environment. See internal/sso/secret.go.
	BearerFile string `yaml:"bearer_file,omitempty" json:"bearer_file,omitempty"`
}

// Duration is a YAML scalar like `10s`, parsed by time.ParseDuration.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(b []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(b)), `"'`)
	if raw == "" {
		return nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) String() string { return time.Duration(d).String() }

// Parse decodes and validates the configuration.
//
// Strict: unknown and duplicate keys are refused, the same choice usersync
// makes for roster.yaml and for the same reason. A typo in a file that decides
// who gets an account must not be read as "that constraint was not there".
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.NewDecoder(strings.NewReader(string(data)), yaml.Strict()).Decode(&c); err != nil {
		return nil, fmt.Errorf("provision: parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.Timeout <= 0 {
		c.Timeout = Duration(DefaultTimeout)
	}
	if c.Wait <= 0 {
		c.Wait = Duration(DefaultWait)
	}
	return &c, nil
}

func (c *Config) validate() error {
	var errs []error
	if len(c.Rules) == 0 {
		// Not an error. A file with no rules is how an operator turns the feature
		// off without removing the flag and redeploying, and it must reload
		// cleanly into "nothing matches".
		return nil
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		u, err := url.Parse(r.URL)
		switch {
		case r.URL == "":
			errs = append(errs, fmt.Errorf("rule %d: no url", i))
		case err != nil:
			errs = append(errs, fmt.Errorf("rule %d: url: %w", i, err))
		case u.Scheme != "https" && !isLoopback(u):
			errs = append(errs, fmt.Errorf("rule %d: url must be https (or a loopback address), got %q", i, r.URL))
		}
		for j, d := range r.Match.Domains {
			r.Match.Domains[j] = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "@")))
		}
	}
	return errors.Join(errs...)
}

func isLoopback(u *url.URL) bool {
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Rule returns the first rule matching the identity, or nil.
func (c *Config) Rule(emails []string, claims map[string]any) *Rule {
	if c == nil {
		return nil
	}
	for i := range c.Rules {
		if c.Rules[i].matches(emails, claims) {
			return &c.Rules[i]
		}
	}
	return nil
}

func (r *Rule) matches(emails []string, claims map[string]any) bool {
	if len(r.Match.Domains) > 0 {
		ok := false
		for _, e := range emails {
			_, domain, _ := strings.Cut(strings.ToLower(e), "@")
			for _, d := range r.Match.Domains {
				if domain == d {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	for name, want := range r.Match.Claims {
		if !claimHas(claims[name], want) {
			return false
		}
	}
	return true
}

// claimHas reports whether a claim satisfies a required value.
//
// A scalar must equal it; a list must contain it. The list case is what makes a
// groups or roles claim usable, and those are the claims anybody would actually
// want to gate on.
func claimHas(got any, want string) bool {
	switch v := got.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == want {
				return true
			}
		}
	case float64:
		// JSON numbers arrive as float64; compare as written rather than
		// pretending a numeric claim cannot be matched.
		return strings.TrimSuffix(fmt.Sprintf("%v", v), ".0") == want
	case bool:
		return fmt.Sprintf("%v", v) == want
	}
	return false
}
