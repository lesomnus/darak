package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `
timeout: 5s
wait: 1m
rules:
  - name: 연구소
    match:
      domains: ["@Example.com"]
      claims: { department: research }
    url: https://hr.example.com/provision
    auth:
      bearer_file: /run/secrets/token
  - name: everyone else
    url: https://hr.example.com/provision-guest
`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(c.Timeout) != 5*time.Second || time.Duration(c.Wait) != time.Minute {
		t.Errorf("durations = %v / %v", c.Timeout, c.Wait)
	}
	// The leading @ and the capitals are normalised, so a file written the way
	// people write addresses still matches.
	if c.Rules[0].Match.Domains[0] != "example.com" {
		t.Errorf("domain = %q; want it normalised", c.Rules[0].Match.Domains[0])
	}
}

// A typo in a file that decides who gets an account must not read as "that
// constraint was not there".
func TestParseIsStrict(t *testing.T) {
	for name, in := range map[string]string{
		"unknown key":     "rules:\n  - url: https://x.example\n    matchh: {}\n",
		"unknown top key": "timeoutt: 5s\nrules: []\n",
		"bad duration":    "timeout: soon\nrules: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Error("accepted a file it should have refused")
			}
		})
	}
}

// The endpoint receives an assertion about somebody's identity and answers with
// something that can create an account. Plain http on the network is not a
// place for that conversation; a loopback sidecar has no network to protect.
func TestParseRefusesPlainHTTPButAllowsLoopback(t *testing.T) {
	if _, err := Parse([]byte("rules:\n  - url: http://hr.example.com/x\n")); err == nil {
		t.Error("plain http to another host was accepted")
	}
	if _, err := Parse([]byte("rules:\n  - url: http://localhost:9000/x\n")); err != nil {
		t.Errorf("a loopback sidecar was refused: %v", err)
	}
}

// An empty rule list is how the feature is turned off without redeploying, so
// it has to load cleanly into "nothing matches".
func TestParseAcceptsNoRules(t *testing.T) {
	c, err := Parse([]byte("rules: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Rule([]string{"a@example.com"}, nil) != nil {
		t.Error("a rule matched in an empty configuration")
	}
}

func TestRuleMatching(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"department": "research"}

	// First match wins, and it is the narrow one.
	if got := c.Rule([]string{"a@example.com"}, claims); got == nil || got.Name != "연구소" {
		t.Errorf("Rule() = %+v; want the department rule", got)
	}
	// Right domain, wrong department: falls through to the open rule.
	if got := c.Rule([]string{"a@example.com"}, map[string]any{"department": "sales"}); got == nil || got.Name != "everyone else" {
		t.Errorf("Rule() = %+v; want the fallback", got)
	}
}

// Gating on a groups claim is the case anybody would actually want, and those
// claims arrive as lists.
func TestClaimMatchingHandlesLists(t *testing.T) {
	c, err := Parse([]byte("rules:\n  - url: https://x.example\n    match:\n      claims: { groups: file-server }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Rule(nil, map[string]any{"groups": []any{"other", "file-server"}}) == nil {
		t.Error("a list claim containing the value did not match")
	}
	if c.Rule(nil, map[string]any{"groups": []any{"other"}}) != nil {
		t.Error("a list claim without the value matched anyway")
	}
	if c.Rule(nil, nil) != nil {
		t.Error("a missing claim matched")
	}
}

// The reload rule, and the reason it differs from startup: at startup there is
// no last-good version to keep.
func TestReloadKeepsTheLastGoodConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provision.yaml")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Config().Rules) != 2 {
		t.Fatalf("loaded %d rules", len(w.Config().Rules))
	}
	first := w.Status().Digest

	if err := os.WriteFile(path, []byte("rules:\n  - url: https://x.example\n    nonsense: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.reload(); err == nil {
		t.Fatal("a malformed file reloaded without complaint")
	}
	if len(w.Config().Rules) != 2 {
		t.Error("a malformed file changed the rules in force")
	}
	st := w.Status()
	if st.Error == "" {
		t.Error("the failure is not reported anywhere an operator would see it")
	}
	if st.Digest != first {
		t.Error("the reported digest moved to a version that was refused")
	}

	// And a good edit lands.
	if err := os.WriteFile(path, []byte("rules:\n  - url: https://y.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.reload(); err != nil {
		t.Fatal(err)
	}
	if got := w.Config().Rules; len(got) != 1 || got[0].URL != "https://y.example" {
		t.Errorf("rules after a good edit = %+v", got)
	}
	if w.Status().Error != "" {
		t.Error("the previous failure is still reported after a good load")
	}
}

func TestWatcherRefusesToStartOnABadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provision.yaml")
	if err := os.WriteFile(path, []byte("rules:\n  - url: nonsense\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWatcher(path, time.Hour); err == nil {
		t.Fatal("started with rules that cannot be used, and nothing would say why")
	}
}

// The status is what the operator page renders, so it must carry the path a
// token is read from and never the token.
func TestStatusCarriesNoSecret(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "provision.yaml")
	if err := os.WriteFile(path, []byte(
		"rules:\n  - url: https://x.example\n    auth:\n      bearer_file: "+tokenFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	st := w.Status()
	if st.Rules[0].Auth.BearerFile != tokenFile {
		t.Errorf("the token path is missing from the status: %+v", st.Rules[0].Auth)
	}
	if strings.Contains(strings.Join([]string{st.Digest, st.Path, st.Rules[0].URL, st.Rules[0].Auth.BearerFile}, " "), "s3cret") {
		t.Error("the token itself reached the status")
	}
}
