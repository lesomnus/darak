package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/darak/control/controlpb"
)

// Team membership, delegated.
//
// Two authorities meet here and they are deliberately not the same one.
// Membership in the `admin` POSIX group opens the operator page and everything
// on it. Being listed in a group's `owners` opens exactly one thing: adding and
// removing members of THAT group. An owner is not a small administrator, and an
// administrator does not become an owner by being one — an admin who is also
// meant to run a team is listed in its owners like anybody else.
//
// The declaration is read from `usersync roster`, not from the system, and not
// from /etc/gshadow where usersync mirrors it. gshadow normally agrees, but
// "normally" is not what an authorization check should rest on, and anyone able
// to write gshadow could otherwise grant themselves the delegation.

// ErrNotOwner is returned when the caller does not own the group.
var ErrNotOwner = errors.New("admin: not an owner of this team")

// Declaration is the roster as written, as `usersync roster` prints it.
type Declaration struct {
	Groups []DeclaredGroup `json:"groups"`
	Users  []DeclaredUser  `json:"users"`
}

type DeclaredGroup struct {
	Name        string   `json:"name"`
	GID         uint32   `json:"gid"`
	Description string   `json:"description,omitempty"`
	Owners      []string `json:"owners"`
	// Anonymous is the folder's unauthenticated-access level: "none", "read", or
	// "write". It is what tells the interface a folder is public, so an anonymous
	// visitor can be shown where to look; the kernel still enforces the access.
	Anonymous string `json:"anonymous,omitempty"`
}

type DeclaredUser struct {
	Name     string   `json:"name"`
	UID      uint32   `json:"uid"`
	FullName string   `json:"full_name,omitempty"`
	Groups   []string `json:"groups"`
	Status   string   `json:"status"`
}

// Declaration reads the roster.
func (a *Admin) Declaration(ctx context.Context) (*Declaration, error) {
	out, err := a.cfg.Runner.Run(ctx, "", a.cfg.UsersyncBin, "roster")
	if err != nil {
		return nil, fmt.Errorf("admin: read the roster: %w", err)
	}
	// usersync logs to stderr but a warning can still reach stdout ahead of the
	// report, so start at the object.
	start := strings.IndexByte(out, '{')
	if start < 0 {
		return nil, fmt.Errorf("admin: the roster output had no JSON in it")
	}
	var d Declaration
	if err := json.Unmarshal([]byte(out[start:]), &d); err != nil {
		return nil, fmt.Errorf("admin: parse the roster: %w", err)
	}
	return &d, nil
}

// OwnedTeams lists the groups the user may manage the membership of.
//
// Admins are NOT folded in. A page that showed an admin as the owner of every
// team would make "who is responsible for this team" unanswerable, and the two
// permissions have different shapes: an admin may do everything to accounts
// except this, and an owner may do only this.
func (a *Admin) OwnedTeams(ctx context.Context, user string) ([]string, error) {
	d, err := a.Declaration(ctx)
	if err != nil {
		return nil, err
	}
	owned := []string{}
	for _, g := range d.Groups {
		if slices.Contains(g.Owners, user) {
			owned = append(owned, g.Name)
		}
	}
	slices.Sort(owned)
	return owned, nil
}

// PublicFolder is one folder open to anonymous visitors.
type PublicFolder struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Write is true when the folder is anonymous:write (anyone may write, not
	// only read), so the interface can say which it is.
	Write bool `json:"write"`
}

// PublicFolders lists the folders the roster opens to anonymous visitors, sorted
// by name. It reads the declaration, not the system: a folder is public because
// the roster says so, which is also what usersync used to set the mode bits the
// kernel enforces.
func (a *Admin) PublicFolders(ctx context.Context) ([]PublicFolder, error) {
	d, err := a.Declaration(ctx)
	if err != nil {
		return nil, err
	}
	out := []PublicFolder{}
	for _, g := range d.Groups {
		switch g.Anonymous {
		case "read", "write":
			out = append(out, PublicFolder{Name: g.Name, Description: g.Description, Write: g.Anonymous == "write"})
		}
	}
	slices.SortFunc(out, func(x, y PublicFolder) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

// MayManageTeam reports whether the actor can change the team's membership.
//
// An admin may, because the operator page is already the place accounts are
// managed from and withholding this one operation from the person who can reset
// everyone's password would be a distinction without a difference.
func (a *Admin) MayManageTeam(ctx context.Context, actor, team string) (bool, error) {
	admin, err := a.IsAdmin(ctx, actor)
	if err != nil {
		return false, err
	}
	if admin {
		return true, nil
	}
	owned, err := a.OwnedTeams(ctx, actor)
	if err != nil {
		return false, err
	}
	return slices.Contains(owned, team), nil
}

// SetTeamMembership adds or removes a member, on behalf of actor.
//
// The write goes through `usersync member`, which edits the roster's syntax
// tree so the change is one line, validates the result before writing so a bad
// request cannot leave a roster the next boot refuses, and takes a lock so two
// owners editing at once do not lose one another's change.
//
// `usersync apply` then converges the system. It runs here, synchronously,
// because the alternative is a request that succeeds while nothing happens
// until the next restart — and the person who clicked is the only one in a
// position to notice that it did not work.
func (a *Admin) SetTeamMembership(ctx context.Context, actor, team, user string, member bool) error {
	ok, err := a.MayManageTeam(ctx, actor, team)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotOwner, team)
	}

	// Both names must be things the roster declares. `usersync member` checks
	// this too and is the authority; checking here as well keeps a bad request
	// from being reported as a tool failure.
	d, err := a.Declaration(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(d.Users, func(u DeclaredUser) bool { return u.Name == user }) {
		return fmt.Errorf("%w: %q", ErrUnknownUser, user)
	}

	op := "remove"
	if member {
		op = "add"
	}

	// Through the control plane when one is configured: darak has decided the
	// caller may, and the control plane edits the roster's source and lands the
	// change (usersync then reconciles, downstream). Adding puts the account in
	// as an ordinary writing member; a later re-grade to reader/owner is Grade,
	// which the current team panel does not yet drive.
	if a.cfg.Controller != nil {
		var err error
		if member {
			_, err = a.cfg.Controller.Membership.Add(ctx, &controlpb.AddMembershipRequest{
				Account: user, Group: team, Role: controlpb.Role_ROLE_MEMBER,
			})
		} else {
			_, err = a.cfg.Controller.Membership.Erase(ctx, &controlpb.EraseMembershipRequest{
				Account: user, Group: team,
			})
		}
		if err != nil {
			return fmt.Errorf("admin: %s %q %s team %q: %w", op, user, prep(member), team, err)
		}
		return nil
	}

	// No control plane: edit the roster here with `usersync member`. This needs a
	// writable roster on the host and is the pre-control-plane path.
	if _, err := a.cfg.Runner.Run(ctx, "", a.cfg.UsersyncBin, "member", op, user, team); err != nil {
		return fmt.Errorf("admin: %s %q %s team %q: %w", op, user, prep(member), team, err)
	}

	// Converge. A failure here is reported but NOT rolled back: the roster is
	// the desired state and it now says the right thing, so the correct recovery
	// is another apply rather than an edit that puts the declaration back to
	// something nobody asked for.
	if _, err := a.cfg.Runner.Run(ctx, "", a.cfg.UsersyncBin, "apply"); err != nil {
		return fmt.Errorf("admin: the roster was updated but the system was not converged (run `usersync apply`): %w", err)
	}
	return nil
}

func prep(member bool) string {
	if member {
		return "to"
	}
	return "from"
}

// TeamView is one team as its manager sees it.
type TeamView struct {
	Name        string   `json:"name"`
	GID         uint32   `json:"gid"`
	Description string   `json:"description,omitempty"`
	Owners      []string `json:"owners"`
	Members     []string `json:"members"`
}

// TeamsView is what the team panel renders from.
type TeamsView struct {
	Teams []TeamView `json:"teams"`
	// Users is every account that could be added, so the interface does not need
	// the full inventory — which an owner cannot read, being no administrator.
	Users []string `json:"users"`
}

// ManageableTeams returns the teams the actor may change, with their membership.
//
// Built from the DECLARATION rather than the system for the same reason the
// authorization is: this is the list the caller is about to edit, and showing
// them the system's version would mean the "remove" button next to a name that
// the roster does not have there.
func (a *Admin) ManageableTeams(ctx context.Context, actor string) (*TeamsView, error) {
	d, err := a.Declaration(ctx)
	if err != nil {
		return nil, err
	}
	admin, err := a.IsAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}

	view := &TeamsView{Teams: []TeamView{}, Users: []string{}}
	for _, u := range d.Users {
		// A reserved entry has no account, so it cannot be put on a team.
		if u.Status != "reserved" {
			view.Users = append(view.Users, u.Name)
		}
	}
	slices.Sort(view.Users)

	for _, g := range d.Groups {
		if !admin && !slices.Contains(g.Owners, actor) {
			continue
		}
		members := []string{}
		for _, u := range d.Users {
			if slices.Contains(u.Groups, g.Name) {
				members = append(members, u.Name)
			}
		}
		slices.Sort(members)
		view.Teams = append(view.Teams, TeamView{
			Name:        g.Name,
			GID:         g.GID,
			Description: g.Description,
			Owners:      g.Owners,
			Members:     members,
		})
	}
	return view, nil
}
