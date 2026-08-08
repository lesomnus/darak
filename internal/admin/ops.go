package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The operations here change tdbsam and nothing else.
//
// That boundary is not an implementation detail, it is the design: roster.yaml
// reserves a uid forever and its git history is the record of who was given
// what, so anything that would edit it belongs in a reviewed commit and a
// restart. Suspending an SMB account and resetting an SMB password are neither
// — they are the day-to-day operations that leave the ledger exactly as it was,
// which is why they can be done from a web page at all.
//
// What deliberately has no method here: creating a user, deleting one, changing
// a uid, and changing team membership. The first three would invent numbers the
// roster does not know; the fourth would make `usersync plan` want to undo it
// at the next restart, which is a worse outcome than not offering the button.

// ErrUnknownUser is returned for a name that is not a managed account.
var ErrUnknownUser = errors.New("admin: not a managed account")

// managed reports whether the name is one of the accounts this server manages.
//
// Every operation checks it first. Without it the admin group would be able to
// aim smbpasswd at any name Samba knows, including a service account that has
// nothing to do with this deployment — the page would be a general-purpose
// Samba console that happens to be reachable over HTTP.
func (a *Admin) managed(ctx context.Context, user string) error {
	if user == "" || strings.ContainsAny(user, ":/\n\x00 ") {
		return fmt.Errorf("%w: %q", ErrUnknownUser, user)
	}
	inv, err := a.Inventory(ctx)
	if err != nil {
		return err
	}
	for _, u := range inv.Users {
		if u.Name == user {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownUser, user)
}

// SetSMBEnabled enables or disables a user's Samba account.
//
// This is the reversible half of offboarding: the account, the home and the uid
// all stay exactly where they are and only the ability to authenticate goes
// away, so a return from leave is one call back the other way. Deleting the
// account instead would free nothing and lose the password.
func (a *Admin) SetSMBEnabled(ctx context.Context, user string, enabled bool) error {
	if err := a.managed(ctx, user); err != nil {
		return err
	}
	flag := "-d"
	if enabled {
		flag = "-e"
	}
	if _, err := a.cfg.Runner.Run(ctx, "", a.cfg.SmbpasswdBin, flag, user); err != nil {
		return fmt.Errorf("admin: set SMB state for %q: %w", user, err)
	}
	return nil
}

// SetSMBPassword sets a user's Samba password.
//
// The password goes in on stdin, never as an argument: an argv is readable by
// anyone who can list processes, and run.Exec's error formatting deliberately
// includes the command line but never stdin.
//
// smbpasswd -s reads the new password twice, which is why it is sent twice.
func (a *Admin) SetSMBPassword(ctx context.Context, user, password string) error {
	if err := a.managed(ctx, user); err != nil {
		return err
	}
	if password == "" {
		return errors.New("admin: refusing to set an empty password")
	}
	if strings.ContainsAny(password, "\n\x00") {
		// smbpasswd reads line-delimited input, so a newline would silently make
		// the stored password a prefix of what was asked for.
		return errors.New("admin: password must not contain a newline")
	}
	stdin := password + "\n" + password + "\n"
	if _, err := a.cfg.Runner.Run(ctx, stdin, a.cfg.SmbpasswdBin, "-s", "-a", user); err != nil {
		return fmt.Errorf("admin: set SMB password for %q: %w", user, err)
	}
	return nil
}
