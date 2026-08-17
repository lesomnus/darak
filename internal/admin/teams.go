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
	// Readers are other groups whose members may READ this group's folder but not
	// write it. Used to compute who may enter a team folder without probing the
	// filesystem — a reader is not a member but can still open it.
	Readers []string `json:"readers,omitempty"`
	// All marks a group that contains every active user (usersync maintains the
	// membership). A registered user is implicitly in it, which is how a folder
	// that reads it via `readers` is open to everyone signed in but not anonymous.
	All bool `json:"all,omitempty"`
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

// TeamAccess reports, per declared team, whether user may ENTER its folder.
//
// It is derived from the roster, not by opening (or stat-ing) each team folder:
// the answer is a set intersection over data already parsed, so a listing of the
// `teams` root costs one roster read instead of a probe per team. A folder is
// enterable when it is world-open (anonymous read or write), when the user is in
// the team group, or when the user is in one of the team's reader groups.
//
// This is a DISPLAY hint only — a lock in the listing instead of a permission
// error on the click. The kernel still decides on every open, so a stale or
// wrong answer here mislabels an icon and grants nothing. That is also why it is
// uniform: an admin is not special for reading a team's files (there is no
// superuser), so an admin sees the same locks anyone else would.
func (a *Admin) TeamAccess(ctx context.Context, user string) (map[string]bool, error) {
	d, err := a.Declaration(ctx)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, u := range d.Users {
		if u.Name == user {
			for _, g := range u.Groups {
				mine[g] = true
			}
			break
		}
	}
	// Every active user belongs to the `all` groups without the roster listing it,
	// so the caller — a signed-in user — is in each. Folding them in here is what
	// lets a folder that reads an `all` group show as open rather than locked.
	for _, g := range d.Groups {
		if g.All {
			mine[g.Name] = true
		}
	}
	out := make(map[string]bool, len(d.Groups))
	for _, g := range d.Groups {
		access := g.Anonymous == "read" || g.Anonymous == "write" || mine[g.Name]
		for _, r := range g.Readers {
			if access {
				break
			}
			access = mine[r]
		}
		out[g.Name] = access
	}
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
// darak authorizes and validates here, then hands the write to the control
// plane. The write itself is `usersync member` — which edits the roster's syntax
// tree so the change is one line, validates the result before writing so a bad
// request cannot leave a roster the next boot refuses, and takes a lock so two
// owners editing at once do not lose one another's change — followed by
// `usersync apply` to converge the system. Locally that runs on this host; behind
// a sidecar it runs there against a repository. Either way it is synchronous, so
// the request does not succeed while nothing happens until the next restart.
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

	// darak has decided the caller may; the control plane does the writing. It
	// edits the roster (a local file, or a repository behind a sidecar — the
	// Controller was built one way or the other) and converges, and usersync
	// reconciles the system to it. Adding puts the account in as an ordinary
	// writing member; a later re-grade to reader/owner is Grade, which the
	// current team panel does not yet drive.
	if err := a.membership(); err != nil {
		return err
	}
	var cerr error
	if member {
		_, cerr = a.cfg.Controller.Membership.Add(ctx, &controlpb.AddMembershipRequest{
			Account: user, Group: team, Role: controlpb.Role_ROLE_MEMBER,
		})
	} else {
		_, cerr = a.cfg.Controller.Membership.Erase(ctx, &controlpb.EraseMembershipRequest{
			Account: user, Group: team,
		})
	}
	if cerr != nil {
		return fmt.Errorf("admin: %s %q %s team %q: %w", op, user, prep(member), team, cerr)
	}
	return nil
}

// membership reports the control plane can change group membership, so a caller
// gets a clear error instead of a nil-pointer panic on a deployment wired
// without one.
func (a *Admin) membership() error {
	if a.cfg.Controller == nil || a.cfg.Controller.Membership == nil {
		return errors.New("admin: no control plane configured to change team membership")
	}
	return nil
}

func prep(member bool) string {
	if member {
		return "to"
	}
	return "from"
}

// MembershipChange is one staged edit to a team's membership: add User to Team,
// or remove them. It is what the page accumulates so several can be confirmed
// together.
type MembershipChange struct {
	Team   string `json:"team"`
	User   string `json:"user"`
	Member bool   `json:"member"` // true to add, false to remove
}

// BatchSetTeamMembership applies several membership changes as one, on behalf of
// actor.
//
// The point of doing it in one call is the same one that made a single change go
// through the control plane: the roster is version-controlled and reconciled
// downstream, so N separate edits are N commits and N syncs. An operator who
// stages a handful of changes and confirms them wants one commit and one
// convergence to watch — so the whole batch is authorized, validated, and then
// handed down together, and it lands or it does not as a whole.
//
// Authorization is per-team and total: if the actor may not manage even one of
// the teams named, nothing is done. A team owner is not an administrator, and a
// batch is not a loophole for touching a team they do not own by burying it
// among ones they do.
func (a *Admin) BatchSetTeamMembership(ctx context.Context, actor string, changes []MembershipChange) error {
	if len(changes) == 0 {
		return nil
	}

	// One read of the roster serves both the authorization and the validation
	// below; the control plane re-checks everything downstream regardless.
	d, err := a.Declaration(ctx)
	if err != nil {
		return err
	}
	users := map[string]bool{}
	for _, u := range d.Users {
		users[u.Name] = true
	}
	owners := map[string][]string{}
	for _, g := range d.Groups {
		owners[g.Name] = g.Owners
	}

	admin, err := a.IsAdmin(ctx, actor)
	if err != nil {
		return err
	}
	for _, c := range changes {
		if _, ok := owners[c.Team]; !ok {
			// An unknown team reads as one the actor does not own — a stranger learns
			// nothing about which teams exist.
			return fmt.Errorf("%w: %q", ErrNotOwner, c.Team)
		}
		if !admin && !slices.Contains(owners[c.Team], actor) {
			return fmt.Errorf("%w: %q", ErrNotOwner, c.Team)
		}
		if !users[c.User] {
			return fmt.Errorf("%w: %q", ErrUnknownUser, c.User)
		}
	}

	// Hand the whole batch to the control plane, which lands it as one change (a
	// single commit behind a sidecar, or one `usersync apply` locally) so the
	// downstream reconcile converges once, not once per edit. Adds go in as
	// ordinary writing members.
	if err := a.membership(); err != nil {
		return err
	}
	batch := make([]*controlpb.MembershipChange, 0, len(changes))
	for _, c := range changes {
		op := controlpb.MembershipChange_OP_ERASE
		if c.Member {
			op = controlpb.MembershipChange_OP_ADD
		}
		batch = append(batch, &controlpb.MembershipChange{
			Op: op, Account: c.User, Group: c.Team, Role: controlpb.Role_ROLE_MEMBER,
		})
	}
	if _, err := a.cfg.Controller.Membership.Batch(ctx, &controlpb.BatchMembershipsRequest{Changes: batch}); err != nil {
		return fmt.Errorf("admin: apply %d membership changes: %w", len(changes), err)
	}
	return nil
}

// MembershipApplied reports whether the running system already reflects every
// change — the signal a status view waits on. It reads NSS (`getent group`), not
// the roster: a change is "applied" once the pipeline it started (commit → the
// roster file syncs in → usersync converges the system) has actually reached the
// group table, which is the thing the person who made the change is waiting to
// become true. An add is applied when the account appears among the group's
// members; a remove, when it no longer does.
func (a *Admin) MembershipApplied(ctx context.Context, changes []MembershipChange) (bool, error) {
	// Cache each group's members across the changes so a batch touching one team
	// reads it once.
	seen := map[string][]string{}
	for _, c := range changes {
		members, ok := seen[c.Team]
		if !ok {
			m, _, err := a.groupMembers(ctx, c.Team)
			if err != nil {
				return false, err
			}
			members = m
			seen[c.Team] = m
		}
		has := slices.Contains(members, c.User)
		if has != c.Member {
			return false, nil
		}
	}
	return true, nil
}

// groupMembers lists a group's supplementary members from NSS, keyed by name for
// the same reason lookupGroupGID is. A group NSS does not know yet is reported as
// absent, not an error — the reconcile simply has not got there.
func (a *Admin) groupMembers(ctx context.Context, name string) (members []string, found bool, err error) {
	out, err := a.cfg.Runner.Run(ctx, "", "getent", "group", name)
	if err != nil || strings.TrimSpace(out) == "" {
		return nil, false, nil
	}
	// name:x:gid:members — members is a comma list, possibly empty.
	f := strings.SplitN(strings.TrimSpace(out), ":", 4)
	if len(f) < 4 {
		return []string{}, true, nil
	}
	for _, m := range strings.Split(f[3], ",") {
		if m = strings.TrimSpace(m); m != "" {
			members = append(members, m)
		}
	}
	return members, true, nil
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
