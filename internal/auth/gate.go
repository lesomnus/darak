package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/lesomnus/darak/internal/helperpool"
)

// A Gate answers whether an account may sign in at all — separately from how
// the person proved they are that account.
//
// The password path never needed this. ntlm_auth answers "no" for a disabled
// account and for a name with no account, so the two questions arrived
// together. An identity provider answers a different question entirely: it says
// who somebody is in the directory, and knows nothing about whether this file
// server still has an account for them. Without something asking here, an
// approved mapping would outlive the account it points at — which is exactly
// the split ADR-2 refused to create when it kept one credential store.
//
// So this asks the SAME store the password path asks. `status: disabled` in
// roster.yaml becomes a `D` flag in tdbsam at the next apply, and that one line
// closes SMB, the password login, and this. Nothing here is a second opinion
// about who may enter; it is the same opinion, requested over a different wire.
type Gate struct {
	// Resolver answers whether the name is an account the system knows, which is
	// the same lookup the helper pool does before starting a helper. A name that
	// does not resolve could not be served anyway: there is no uid to become.
	Resolver Resolver

	// Runner and PdbeditBin ask tdbsam. Empty PdbeditBin means "pdbedit" on PATH.
	Runner     Runner
	PdbeditBin string
}

// Resolver looks up a user's credentials. It is the pool's own interface rather
// than a narrower one declared here, so the gate cannot be satisfied by
// something the pool would refuse: "this name resolves" has to mean the same
// thing in both places or the gate would pass names no helper could be started
// for.
type Resolver = helperpool.Resolver

// Verdict is the gate's answer.
//
// Reason is filled in only when Allowed is false, and it is for the operator's
// log rather than for the person signing in. Telling a caller which of "no such
// account" and "account suspended" applies would answer a question about
// somebody else's account to whoever can reach the login page.
type Verdict struct {
	Allowed bool
	Reason  string
}

// MaySignIn reports whether user may open a session right now.
//
// It fails CLOSED but distinguishes the failures: an error means the question
// could not be asked, and the caller must not read that as permission. This
// mirrors ErrUnavailable on the password path, and for the same reason — a
// broken passdb that reads as "denied" sends whoever is on call to the wrong
// system.
func (g Gate) MaySignIn(ctx context.Context, user string) (Verdict, error) {
	if !namePattern.MatchString(user) {
		return Verdict{Reason: "not a possible account name"}, nil
	}

	if _, err := g.Resolver.Resolve(ctx, user); err != nil {
		// Not an error to report upward: a name that NSS does not know is a
		// definite answer, and the usual cause is a mapping left behind after the
		// account was purged from the roster.
		return Verdict{Reason: "no such account on this server"}, nil
	}

	bin := g.PdbeditBin
	if bin == "" {
		bin = "pdbedit"
	}
	out, err := g.Runner.Run(ctx, "", bin, "-L", "-v")
	if err != nil {
		return Verdict{}, fmt.Errorf("%w: pdbedit: %v", ErrUnavailable, err)
	}
	enabled, ok := ParseAccountFlags(out)[user]
	if !ok {
		return Verdict{Reason: "no SMB account"}, nil
	}
	if !enabled {
		return Verdict{Reason: "account is suspended"}, nil
	}
	return Verdict{Allowed: true}, nil
}

// Exists reports whether the name is an account the system knows at all,
// regardless of whether it may sign in.
//
// Deliberately a different question from MaySignIn, and asked by provisioning
// for the one thing MaySignIn cannot answer: a suspended account is not
// allowed in, but it very much already EXISTS, and a hook claiming to have just
// created that name must not be believed. Folding the two together would let a
// disabled account be re-provisioned to whoever asked.
func (g Gate) Exists(ctx context.Context, user string) bool {
	if !namePattern.MatchString(user) {
		return false
	}
	_, err := g.Resolver.Resolve(ctx, user)
	return err == nil
}

// ParseAccountFlags reports which names have a tdbsam account and whether it is
// enabled. `pdbedit -L -v` prints an "Account Flags" field where D means
// disabled — the same bit `smbpasswd -d` sets, and the bit `usersync apply`
// sets from `status: disabled`.
func ParseAccountFlags(out string) map[string]bool {
	accounts := map[string]bool{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "Unix username":
			current = value
			// Present until the flags say otherwise; a record with no flags line
			// is still an account.
			accounts[current] = true
		case "Account Flags":
			if current != "" {
				accounts[current] = !strings.Contains(value, "D")
			}
		}
	}
	return accounts
}

// ValidName reports whether s could be a managed account name.
//
// Exported so the identity mapping can refuse a name that could never belong to
// an account before it is ever written down, rather than storing it and
// producing an unexplainable failure at sign-in.
func ValidName(s string) bool { return namePattern.MatchString(s) }
