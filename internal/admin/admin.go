// Package admin answers the questions an operator has about the server, and
// performs the small set of account operations that do not touch the roster.
//
// The split matters. roster.yaml is a version-controlled, read-only input: it
// is the ledger that pins which uid belongs to whom, and its history IS the
// account-management history (nas-design.md ADR-9). Creating or removing a user
// is therefore an edit to that file followed by a restart, and nothing here can
// do it. What is here is everything that leaves the ledger untouched — reading
// state, and the SMB-side lifecycle (reset a password, suspend an account)
// which lives in tdbsam rather than in the roster.
//
// Membership in one POSIX group decides who may call any of this. Not a table
// in this application: the same group the kernel would use, resolved the same
// way every other permission decision in darak is resolved, so an admin who is
// removed from the group loses the page without anything here being told.
package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/run"
)

// DefaultGroup is the POSIX group whose members may use this package.
const DefaultGroup = "admin"

// ErrNotAdmin is returned when the caller is not in the admin group.
var ErrNotAdmin = errors.New("admin: not a member of the admin group")

// Config wires an Admin.
type Config struct {
	// Group is the POSIX group granting access. Empty means DefaultGroup.
	Group string

	// Root is the served data tree, used for the capacity report.
	Root string

	// HomeBase is the parent of every user's home, used to attribute usage when
	// the filesystem cannot report it per-uid.
	HomeBase string

	// Resolver looks up a user's uid/gid/groups. Defaults to the same NSS
	// resolver the helper pool uses, so "is this person an admin" is answered by
	// exactly the mechanism that decides what their files let them do.
	Resolver helperpool.Resolver

	// Runner runs the backend tools (usersync, pdbedit, smbpasswd, zfs, du).
	Runner run.Runner

	// UsersyncBin, PdbeditBin and SmbpasswdBin locate those tools. Empty means
	// look them up on PATH by their usual names.
	UsersyncBin  string
	PdbeditBin   string
	SmbpasswdBin string
}

// Admin serves the operator surface.
type Admin struct {
	cfg   Config
	usage *usageCache
}

func New(cfg Config) (*Admin, error) {
	if cfg.Root == "" {
		return nil, errors.New("admin: Root is required")
	}
	if cfg.Group == "" {
		cfg.Group = DefaultGroup
	}
	if cfg.Resolver == nil {
		cfg.Resolver = helperpool.NSSResolver{}
	}
	if cfg.Runner == nil {
		cfg.Runner = run.Exec{}
	}
	if cfg.UsersyncBin == "" {
		cfg.UsersyncBin = "usersync"
	}
	if cfg.PdbeditBin == "" {
		cfg.PdbeditBin = "pdbedit"
	}
	if cfg.SmbpasswdBin == "" {
		cfg.SmbpasswdBin = "smbpasswd"
	}
	return &Admin{cfg: cfg, usage: newUsageCache()}, nil
}

// Group reports which POSIX group grants access.
func (a *Admin) Group() string { return a.cfg.Group }

// IsAdmin reports whether the user is a member of the admin group.
//
// The group is resolved on every call rather than cached at startup, because
// the answer has to be able to become false. A membership removed in the roster
// takes effect at the next restart; one removed with `gpasswd -d` takes effect
// now, and an authorization check that trusted a startup snapshot would keep
// letting them in until someone noticed.
//
// A missing admin group is not an error, it is an answer: nobody is an admin.
// Failing open here would turn "the operator has not created the group yet"
// into "everyone can manage accounts".
func (a *Admin) IsAdmin(ctx context.Context, user string) (bool, error) {
	gid, found, err := a.lookupGroupGID(ctx, a.cfg.Group)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	creds, err := a.cfg.Resolver.Resolve(ctx, user)
	if err != nil {
		return false, err
	}
	return creds.GID == gid || slices.Contains(creds.Groups, gid), nil
}

// lookupGroupGID resolves ONE group name through NSS.
//
// Keyed, not a filter over an enumeration: winbind does not enumerate domain
// groups by default, so an admin group served by a directory is absent from
// `getent group` with no arguments yet resolves perfectly well by name. That
// distinction is the same one usersync's audit had to learn.
func (a *Admin) lookupGroupGID(ctx context.Context, name string) (uint32, bool, error) {
	out, err := a.cfg.Runner.Run(ctx, "", "getent", "group", name)
	if err != nil || strings.TrimSpace(out) == "" {
		// getent exits 2 for "not found", which is not a failure of the lookup.
		return 0, false, nil
	}
	// name:x:gid:members
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) < 3 {
		return 0, false, fmt.Errorf("admin: unexpected getent output for group %q", name)
	}
	gid, err := strconv.ParseUint(f[2], 10, 32)
	if err != nil {
		return 0, false, fmt.Errorf("admin: parse gid for group %q: %w", name, err)
	}
	return uint32(gid), true, nil
}
