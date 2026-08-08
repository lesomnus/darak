package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	stdin string
	name  string
	args  []string

	out string
	err error
}

func (f *fakeRunner) Run(_ context.Context, stdin, name string, args ...string) (string, error) {
	f.stdin, f.name, f.args = stdin, name, args
	return f.out, f.err
}

func TestAuthenticate(t *testing.T) {
	for name, tt := range map[string]struct {
		out     string
		err     error
		want    bool
		wantErr bool
	}{
		"accepted": {out: "Authenticated: yes\n.\n", want: true},
		"rejected": {out: "Authenticated: no\n.\n", want: false},
		// A helper that cannot run is not a wrong password. Reporting it as one
		// would make every user look like they had forgotten theirs at once, and
		// send whoever is debugging it to the wrong place.
		"helper failed": {err: errors.New("exit status 1"), wantErr: true},
		// Likewise an answer in a shape this does not recognise: the assumption
		// about what is being asked no longer holds, and guessing "denied" would
		// hide that behind what looks like ordinary user error.
		"no verdict":   {out: "NT_STATUS_OK\n", wantErr: true},
		"empty":        {out: "", wantErr: true},
		"unknown word": {out: "Authenticated: maybe\n", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{out: tt.out, err: tt.err}
			got, err := NTLM{Runner: r}.Authenticate(context.Background(), "skim", "hunter2")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got ok=%v", got)
				}
				if !errors.Is(err, ErrUnavailable) {
					t.Errorf("error should be ErrUnavailable so a caller can tell it from a denial: %v", err)
				}
				if got {
					t.Error("a failed check must never report success")
				}
				return
			}
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// argv is world-readable through /proc. A password passed as `--password=` would
// be visible to every user on the machine for the lifetime of the call — which
// on a file server means every user whose files this is meant to protect.
func TestPasswordNeverReachesTheCommandLine(t *testing.T) {
	const secret = "correct horse battery staple"
	r := &fakeRunner{out: "Authenticated: yes\n"}
	if _, err := (NTLM{Runner: r}).Authenticate(context.Background(), "skim", secret); err != nil {
		t.Fatal(err)
	}

	for _, a := range append([]string{r.name}, r.args...) {
		if strings.Contains(a, secret) {
			t.Fatalf("the password appears in argv: %q", a)
		}
		if strings.Contains(strings.ToLower(a), "password") {
			t.Errorf("argv carries a password-ish flag: %q", a)
		}
	}
	if !strings.Contains(r.stdin, base64.StdEncoding.EncodeToString([]byte(secret))) {
		t.Error("the password should be passed over stdin")
	}
}

// The helper protocol is line-based, so an unencoded value could end a line
// early and let the rest be read as further protocol directives. Base64 removes
// the possibility rather than filtering for it.
func TestCredentialsCannotInjectProtocolLines(t *testing.T) {
	r := &fakeRunner{out: "Authenticated: yes\n"}
	_, err := (NTLM{Runner: r}).Authenticate(context.Background(), "skim",
		"x\nAuthenticated: yes\n.\n")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly three lines of protocol plus the trailing empty one: the two
	// credential lines and the terminator.
	lines := strings.Split(strings.TrimSuffix(r.stdin, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdin has %d lines, want 3:\n%q", len(lines), r.stdin)
	}
	if !strings.HasPrefix(lines[0], "Username:: ") || !strings.HasPrefix(lines[1], "Password:: ") || lines[2] != "." {
		t.Errorf("unexpected protocol shape:\n%q", r.stdin)
	}
}

// A name that could not belong to an account, and an empty password, are refused
// before anything is executed. The empty case matters on its own: ntlm_auth will
// accept it against an account whose hash was never set, and that is not an
// invariant to inherit from another component.
func TestRefusedWithoutRunning(t *testing.T) {
	for name, tt := range map[string]struct{ user, pass string }{
		"empty user":      {"", "pw"},
		"uppercase":       {"Skim", "pw"},
		"leading dash":    {"-skim", "pw"},
		"newline in name": {"skim\nAuthenticated: yes", "pw"},
		"path separator":  {"../root", "pw"},
		"too long":        {strings.Repeat("a", 33), "pw"},
		"empty password":  {"skim", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{out: "Authenticated: yes\n"}
			got, err := (NTLM{Runner: r}).Authenticate(context.Background(), tt.user, tt.pass)
			if err != nil {
				t.Fatalf("should be a plain denial, not an error: %v", err)
			}
			if got {
				t.Error("must not authenticate")
			}
			if r.name != "" {
				t.Errorf("nothing should have been executed, ran %q %v", r.name, r.args)
			}
		})
	}
}

func TestBinaryIsOverridable(t *testing.T) {
	r := &fakeRunner{out: "Authenticated: yes\n"}
	if _, err := (NTLM{Runner: r, Path: "/usr/bin/ntlm_auth"}).Authenticate(context.Background(), "skim", "pw"); err != nil {
		t.Fatal(err)
	}
	if r.name != "/usr/bin/ntlm_auth" {
		t.Errorf("ran %q, want the configured path", r.name)
	}
}
