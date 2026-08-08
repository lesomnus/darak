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
	"encoding/base64"
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
// The credentials go over stdin in ntlm_auth's ntlm-server-1 helper protocol,
// NOT as command-line arguments: argv is world-readable through /proc, so
// `--password=` would publish every password to every user on the box for the
// lifetime of the call. Both values are base64-encoded, which also means no
// input can introduce the newline that would end a protocol line early.
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

	enc := base64.StdEncoding.EncodeToString
	stdin := fmt.Sprintf("Username:: %s\nPassword:: %s\n.\n", enc([]byte(user)), enc([]byte(password)))

	out, err := a.Runner.Run(ctx, stdin, a.bin(), "--helper-protocol=ntlm-server-1")
	if err != nil {
		// In helper-protocol mode ntlm_auth exits zero and reports the verdict in
		// its output, so a non-zero exit means the helper itself failed — winbindd
		// not running is the usual cause.
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return parseVerdict(out)
}

// parseVerdict reads ntlm-server-1's answer.
//
// Anything other than an explicit "yes" or "no" is an error rather than a
// denial: a format this does not recognise means the assumption about what is
// being asked no longer holds, and guessing "denied" would hide that behind what
// looks like ordinary user error.
func parseVerdict(out string) (bool, error) {
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Authenticated:")
		if !ok {
			continue
		}
		switch strings.TrimSpace(rest) {
		case "yes":
			return true, nil
		case "no":
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: no verdict in ntlm_auth output", ErrUnavailable)
}
