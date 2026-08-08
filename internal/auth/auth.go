// Package auth verifies a user's password.
//
// Per ADR-2 there is one credential store, Samba's passdb, and the web path
// asks it the same question SMB does rather than keeping its own. That is not a
// shortcut: a second store means offboarding has two places to do, and locking
// only one leaves the other working — the web blocked while the SMB password
// still opens the same files.
//
// It also survives the AD transition untouched. ntlm_auth answers from the local
// passdb today and, after a domain join, from AD through winbind. The call does
// not change.
package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrUnavailable means the credential store could not be asked. It is
// deliberately distinct from a wrong password: reporting a broken passdb as bad
// credentials sends whoever is debugging it looking in the wrong place, and
// every user would appear to have forgotten their password at once.
var ErrUnavailable = errors.New("auth: credential store unavailable")

// Authenticator answers whether a password belongs to a user.
//
// It is an interface because ADR-2 lists the conditions that would reopen the
// decision — external exposure, an on-prem AD covering everyone, full IdP
// coverage — and each of them replaces this and nothing else.
type Authenticator interface {
	Authenticate(ctx context.Context, user, password string) (bool, error)
}

// Runner executes a command with stdin and returns its stdout.
type Runner interface {
	Run(ctx context.Context, stdin, name string, args ...string) (string, error)
}

// namePattern is the account-name shape usersync enforces. Checking it here too
// keeps a name that could never be a real account from reaching an exec.
var namePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// NTLM verifies against Samba's passdb by asking ntlm_auth.
type NTLM struct {
	Runner Runner
	// Path is the ntlm_auth binary; empty means "ntlm_auth" on PATH.
	Path string
}

func (a NTLM) bin() string {
	if a.Path == "" {
		return "ntlm_auth"
	}
	return a.Path
}

// Authenticate reports whether the password is correct.
//
// The credentials go over stdin, NOT as command-line arguments: argv is
// world-readable through /proc, so `--password=` would publish every password to
// every user on the box for the lifetime of the call — on a file server, to
// exactly the people its permissions exist to separate.
//
// The protocol is squid-2.5-basic, which is ntlm_auth's PLAINTEXT helper. The
// obvious-sounding ntlm-server-1 is not: it speaks NTLM challenge/response, and
// handing it a username and password answers "Authenticated: No" for a correct
// password, which is the most misleading possible failure. Measured against
// Samba 4.22; see internal/integration for the test that pins it.
//
// Both fields are percent-encoded because ntlm_auth unescapes them
// unconditionally. That makes the encoding a correctness requirement rather than
// a precaution: a password containing a literal '%' is rejected if sent raw. It
// also removes the space that separates the two fields and the newline that ends
// the line, so neither value can restructure the request.
func (a NTLM) Authenticate(ctx context.Context, user, password string) (bool, error) {
	if !namePattern.MatchString(user) {
		// Not an authentication failure to report upward as one — this name could
		// not belong to an account at all.
		return false, nil
	}
	if password == "" {
		// ntlm_auth would accept an empty password against an account whose hash
		// is unset. usersync locks the unix password and always registers an SMB
		// one, but this is not a thing to leave to another component's invariants.
		return false, nil
	}

	stdin := escape(user) + " " + escape(password) + "\n"

	out, err := a.Runner.Run(ctx, stdin, a.bin(), "--helper-protocol=squid-2.5-basic")
	if err != nil {
		// The helper exits zero and reports its verdict in the output, so a
		// non-zero exit means the helper itself failed. winbindd not running is
		// the usual cause: ntlm_auth is a winbind client even on a standalone
		// server.
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return parseVerdict(out)
}

// parseVerdict reads the helper's answer: OK, or ERR with an optional message.
//
// Anything else — including squid's BH, "broken helper" — is an error rather
// than a denial. A format this does not recognise means the assumption about
// what is being asked no longer holds, and guessing "denied" would hide that
// behind what looks like ordinary user error. That is not hypothetical: the
// first version of this code spoke the wrong protocol, and every login would
// have been reported as a wrong password.
func parseVerdict(out string) (bool, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	switch {
	case line == "OK" || strings.HasPrefix(line, "OK "):
		return true, nil
	case line == "ERR" || strings.HasPrefix(line, "ERR "):
		return false, nil
	}
	return false, fmt.Errorf("%w: unrecognised ntlm_auth answer %q", ErrUnavailable, line)
}

// escape percent-encodes a credential field.
//
// ntlm_auth unescapes both fields unconditionally, so this is required for
// correctness — a password containing '%' fails without it — and it also makes
// the field separator and the line terminator unrepresentable in a value.
func escape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}
