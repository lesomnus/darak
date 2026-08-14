package admin

import (
	"context"
	"fmt"
	"strings"
)

// InitialPassword returns the seed-derived initial password for a managed user.
//
// It is RECOMPUTED, not read: usersync derives it from the seed and the account
// name, deterministically, and that is the only reason it can be shown at all.
// Nothing stores it. What tdbsam holds is an NT hash, and no amount of
// privilege turns that back into a password — so "show me everyone's current
// password" is not a policy this server declines, it is a question the
// credential store cannot answer.
//
// Which means the value here is only true while the user has not changed
// theirs. usersync prints it either way, and a stale one delivered to somebody
// as "your password" is a support call that looks like a broken server. The
// caller is expected to verify it before showing it; see the handler.
func (a *Admin) InitialPassword(ctx context.Context, user string) (string, error) {
	if err := a.managed(ctx, user); err != nil {
		return "", err
	}
	out, err := a.cfg.Runner.Run(ctx, "", a.cfg.UsersyncBin, "passwd", user)
	if err != nil {
		return "", fmt.Errorf("admin: derive the initial password for %q: %w", user, err)
	}

	// Take the LAST non-empty line. usersync logs to stderr, but a warning can
	// still reach stdout ahead of the value — the roster reader has the same
	// problem and solves it the same way.
	password := ""
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			password = line
		}
	}
	if password == "" || strings.ContainsAny(password, " \t") {
		return "", fmt.Errorf("admin: could not read an initial password for %q out of usersync's output", user)
	}
	return password, nil
}
