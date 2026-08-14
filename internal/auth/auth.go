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

// MinPasswordLength is the only rule this server puts on a new password.
//
// There is no complexity policy on purpose. The rules that demand a symbol and
// a digit are well established to push people toward one predictable pattern
// and a sticky note, and this deployment's real defence is elsewhere: the
// server is not on the internet (ADR-2's stated condition), and an account can
// be shut in one line. A length floor is the part that actually buys something.
const MinPasswordLength = 8

// MaxPasswordLength bounds what will be handed to smbpasswd. Nothing here needs
// a megabyte of password, and an unbounded field is an unbounded argument to a
// subprocess.
const MaxPasswordLength = 256

// ErrWeakPassword means a proposed password cannot be used.
var ErrWeakPassword = errors.New("auth: password rejected")

// PasswordStore changes what tdbsam holds.
//
// The store is Samba's, not this server's: nothing here keeps a password or a
// hash of one, and this type only knows how to ask smbpasswd. That is the whole
// point of ADR-2 — one credential, one place — and it is why changing a
// password from the web changes the SMB password too, because they are not two
// things.
type PasswordStore struct {
	Runner Runner
	// Path is the smbpasswd binary; empty means "smbpasswd" on PATH.
	Path string
}

// CheckPassword reports whether a proposed password is usable.
//
// Exported so a caller can reject one before asking for the current password,
// and so the same rule applies to an operator's reset and to somebody changing
// their own.
func CheckPassword(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLength)
	case len(password) > MaxPasswordLength:
		return fmt.Errorf("%w: at most %d bytes", ErrWeakPassword, MaxPasswordLength)
	}
	// smbpasswd reads line-delimited input, so a newline would silently store a
	// PREFIX of what was asked for — the person would then be unable to sign in
	// with the password they believe they set.
	if strings.ContainsAny(password, "\n\r\x00") {
		return fmt.Errorf("%w: no newlines or null bytes", ErrWeakPassword)
	}
	return nil
}

// Set replaces a user's password.
//
// The password travels on stdin, never in argv, for the reason the ntlm_auth
// call gives: argv is world-readable through /proc, and on a file server that
// means publishing it to exactly the people the permissions exist to separate.
// run.Exec's error text includes the command line and never stdin.
//
// smbpasswd -s reads the new password twice, which is why it is sent twice.
func (p PasswordStore) Set(ctx context.Context, user, password string) error {
	if !namePattern.MatchString(user) {
		return fmt.Errorf("auth: not a possible account name: %q", user)
	}
	if err := CheckPassword(password); err != nil {
		return err
	}
	bin := p.Path
	if bin == "" {
		bin = "smbpasswd"
	}
	stdin := password + "\n" + password + "\n"
	if _, err := p.Runner.Run(ctx, stdin, bin, "-s", "-a", user); err != nil {
		return fmt.Errorf("auth: set the password for %q: %w", user, err)
	}
	return nil
}

// namePattern is the account-name shape usersync enforces (its
// roster.NamePattern). Checking it here too keeps a name that could never be a
// real account from reaching an exec.
//
// The two have to stay in step. A name usersync will create but this refuses is
// an account that exists, mounts over SMB, and cannot sign in to the web -- with
// the same 401 as a wrong password, so nobody can tell why.
//
// The dot is allowed: `firstname.lastname` is what most organisations call
// people. It was excluded once on smb.conf-injection grounds that do not
// survive checking -- a dot cannot close a `[...]` section, only a newline can,
// and shadow-utils refuses those itself. The FIRST character is what does the
// work here: it forbids a leading '-' (which ntlm_auth would read as a flag)
// and a name of '.' or '..'.
var namePattern = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

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
