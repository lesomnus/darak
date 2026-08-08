package auth

import (
	"context"
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
		"accepted":              {out: "OK\n", want: true},
		"accepted with message": {out: "OK user=alice\n", want: true},
		"rejected":              {out: "ERR\n", want: false},
		"rejected with message": {out: "ERR Wrong password\n", want: false},
		// A helper that cannot run is not a wrong password. Reporting it as one
		// would make every user look like they had forgotten theirs at once, and
		// send whoever is debugging it to the wrong place.
		"helper failed": {err: errors.New("exit status 1"), wantErr: true},
		// Likewise an answer in a shape this does not recognise: the assumption
		// about what is being asked no longer holds, and guessing "denied" would
		// hide that behind what looks like ordinary user error.
		"no verdict": {out: "NT_STATUS_OK\n", wantErr: true},
		"empty":      {out: "", wantErr: true},
		// squid's "broken helper" answer, and the reply the WRONG helper protocol
		// gives. The first version of this code spoke ntlm-server-1, which answers
		// "Authenticated: No" to a correct password — reporting that as a denial
		// would have made every login look like a typo.
		"broken helper":  {out: "BH\n", wantErr: true},
		"wrong protocol": {out: "Authenticated: No\n.\n", wantErr: true},
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
	r := &fakeRunner{out: "OK\n"}
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
	if !strings.Contains(r.stdin, "correct%20horse%20battery%20staple") {
		t.Errorf("the password should be passed over stdin, encoded: %q", r.stdin)
	}
}

// The request is one line, with a space between the two fields. Encoding makes
// both of those unrepresentable inside a value, so no credential can restructure
// the request no matter what it contains.
func TestCredentialsCannotRestructureTheRequest(t *testing.T) {
	r := &fakeRunner{out: "OK\n"}
	_, err := (NTLM{Runner: r}).Authenticate(context.Background(), "skim", "x\nbob y\n")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(r.stdin, "\n"); n != 1 {
		t.Errorf("stdin has %d newlines, want exactly the terminator: %q", n, r.stdin)
	}
	if n := strings.Count(r.stdin, " "); n != 1 {
		t.Errorf("stdin has %d spaces, want exactly the field separator: %q", n, r.stdin)
	}
}

// ntlm_auth unescapes both fields unconditionally, so a literal '%' that is not
// encoded arrives as the start of an escape and the password is simply wrong.
// This is a correctness requirement, not only a safety one — and it was found by
// measuring the real helper, not by reading about it.
func TestPercentIsEncoded(t *testing.T) {
	r := &fakeRunner{out: "OK\n"}
	if _, err := (NTLM{Runner: r}).Authenticate(context.Background(), "skim", "a%b"); err != nil {
		t.Fatal(err)
	}
	user, pass, ok := strings.Cut(strings.TrimSuffix(r.stdin, "\n"), " ")
	if !ok {
		t.Fatalf("stdin has no field separator: %q", r.stdin)
	}
	if user != "skim" {
		t.Errorf("user field = %q", user)
	}
	if pass != "a%25b" {
		t.Errorf("password field = %q, want a%%25b", pass)
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
			r := &fakeRunner{out: "OK\n"}
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
	r := &fakeRunner{out: "OK\n"}
	if _, err := (NTLM{Runner: r, Path: "/usr/bin/ntlm_auth"}).Authenticate(context.Background(), "skim", "pw"); err != nil {
		t.Fatal(err)
	}
	if r.name != "/usr/bin/ntlm_auth" {
		t.Errorf("ran %q, want the configured path", r.name)
	}
}
