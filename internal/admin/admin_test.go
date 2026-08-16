package admin

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/lesomnus/darak/control/controlpb"
	"github.com/lesomnus/darak/internal/control"
	"github.com/lesomnus/darak/internal/helperpool"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeMembership is a control-plane MembershipServiceClient that records what it
// was asked. Embedding the interface leaves List/Grade unimplemented — the tests
// here do not call them, and a call would panic rather than pass silently.
type fakeMembership struct {
	controlpb.MembershipServiceClient
	added  []string
	erased []string
}

func (f *fakeMembership) Add(_ context.Context, in *controlpb.AddMembershipRequest, _ ...grpc.CallOption) (*controlpb.Membership, error) {
	f.added = append(f.added, in.GetAccount()+"@"+in.GetGroup()+":"+in.GetRole().String())
	return &controlpb.Membership{Account: in.GetAccount(), Group: in.GetGroup(), Role: in.GetRole()}, nil
}

func (f *fakeMembership) Erase(_ context.Context, in *controlpb.EraseMembershipRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.erased = append(f.erased, in.GetAccount()+"@"+in.GetGroup())
	return &emptypb.Empty{}, nil
}

// With a control plane, a team change goes through it as a Membership Add/Erase,
// and NOT out to `usersync member` on the host.
func TestSetTeamMembershipThroughController(t *testing.T) {
	r, res := teamFixture()
	fake := &fakeMembership{}
	a, err := New(Config{Root: "/srv/data", Runner: r, Resolver: res, Controller: &control.Controller{Membership: fake}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// alice is an admin and owns team-a; carol is a declared user.
	if err := a.SetTeamMembership(ctx, "alice", "team-a", "carol", true); err != nil {
		t.Fatalf("add through controller: %v", err)
	}
	if len(fake.added) != 1 || fake.added[0] != "carol@team-a:ROLE_MEMBER" {
		t.Fatalf("controller Add = %v, want [carol@team-a:ROLE_MEMBER]", fake.added)
	}
	if err := a.SetTeamMembership(ctx, "alice", "team-a", "carol", false); err != nil {
		t.Fatalf("remove through controller: %v", err)
	}
	if len(fake.erased) != 1 || fake.erased[0] != "carol@team-a" {
		t.Fatalf("controller Erase = %v, want [carol@team-a]", fake.erased)
	}
	// The local usersync path must not have run.
	for _, c := range r.calls {
		if strings.HasPrefix(c, "usersync member") || c == "usersync apply" {
			t.Errorf("control-plane path shelled out to %q; it must not", c)
		}
	}
}

// TeamAccess is computed from the roster, not the filesystem: a team is
// enterable when it is world-open (anonymous), when the user is in the group, or
// when the user is in one of its reader groups.
func TestTeamAccess(t *testing.T) {
	const roster = `{"groups":[
		{"name":"perception","gid":10001},
		{"name":"public","gid":19999,"anonymous":"read"},
		{"name":"simulation","gid":10017,"readers":["simulation-readers"]},
		{"name":"simulation-readers","gid":10020}
	],"users":[
		{"name":"alice","uid":3001,"groups":["perception","simulation-readers"]},
		{"name":"bob","uid":3002,"groups":["simulation"]}
	]}`
	a := newTestAdmin(t, &fakeRunner{out: map[string]string{"usersync roster": roster}}, fakeResolver{})

	// alice: perception member, simulation reader, and public is world-open.
	got, err := a.TeamAccess(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	for team, want := range map[string]bool{
		"perception":         true, // member
		"simulation-readers": true, // member
		"simulation":         true, // reader (via simulation-readers)
		"public":             true, // anonymous read = world-open
	} {
		if got[team] != want {
			t.Errorf("alice access[%q] = %v, want %v", team, got[team], want)
		}
	}

	// bob is only a simulation member: perception is closed to him, but the
	// world-open public still is not, and he is no reader of simulation's peers.
	got, _ = a.TeamAccess(context.Background(), "bob")
	for team, want := range map[string]bool{
		"simulation":         true,  // member
		"public":             true,  // world-open
		"perception":         false, // not a member
		"simulation-readers": false, // not a member — being read BY it is not membership
	} {
		if got[team] != want {
			t.Errorf("bob access[%q] = %v, want %v", team, got[team], want)
		}
	}
}

// fakeRunner answers a fixed script of commands, and records what it was asked.
type fakeRunner struct {
	out   map[string]string
	err   map[string]error
	calls []string
	stdin []string
}

func (f *fakeRunner) Run(ctx context.Context, stdin, name string, args ...string) (string, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	f.stdin = append(f.stdin, stdin)
	if err, ok := f.err[key]; ok {
		return f.out[key], err
	}
	return f.out[key], nil
}

type fakeResolver map[string]helperpool.Creds

func (f fakeResolver) Resolve(ctx context.Context, user string) (helperpool.Creds, error) {
	c, ok := f[user]
	if !ok {
		return helperpool.Creds{}, errors.New("no such user")
	}
	return c, nil
}

func newTestAdmin(t *testing.T, r *fakeRunner, res fakeResolver) *Admin {
	t.Helper()
	// The local control plane is the default deployment: team changes go through
	// it as `usersync member` + `usersync apply` on this host, which is what the
	// membership tests observe on the same fake runner.
	a, err := New(Config{Root: "/srv/data", Runner: r, Resolver: res, Controller: control.Local(r, "usersync")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestIsAdminUsesPOSIXGroupMembership(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"getent group admin": "admin:x:2000:root\n"}}
	res := fakeResolver{
		"alice": {UID: 3001, GID: 3001, Groups: []uint32{3001, 2000, 10001}},
		"bob":   {UID: 3002, GID: 3002, Groups: []uint32{3002, 10001}},
		// Someone whose PRIMARY group is admin is just as much an admin.
		"carol": {UID: 3003, GID: 2000, Groups: []uint32{2000}},
	}
	a := newTestAdmin(t, r, res)

	for _, tt := range []struct {
		user string
		want bool
	}{
		{"alice", true},
		{"bob", false},
		{"carol", true},
	} {
		got, err := a.IsAdmin(context.Background(), tt.user)
		if err != nil {
			t.Fatalf("IsAdmin(%q): %v", tt.user, err)
		}
		if got != tt.want {
			t.Errorf("IsAdmin(%q) = %v, want %v", tt.user, got, tt.want)
		}
	}
}

// A missing admin group must mean "nobody", never "everybody". Failing open
// here would turn "the operator has not made the group yet" into an open API.
func TestIsAdminFailsClosedWhenGroupIsAbsent(t *testing.T) {
	r := &fakeRunner{
		out: map[string]string{"getent group admin": ""},
		err: map[string]error{"getent group admin": errors.New("exit status 2")},
	}
	res := fakeResolver{"alice": {UID: 3001, GID: 3001, Groups: []uint32{3001}}}

	got, err := newTestAdmin(t, r, res).IsAdmin(context.Background(), "alice")
	if err != nil {
		t.Fatalf("a missing group is an answer, not an error: %v", err)
	}
	if got {
		t.Error("IsAdmin = true with no admin group on the system")
	}
}

// Membership is re-read per call. A twelve-hour session must not outlive the
// authorization it was granted under.
func TestIsAdminIsNotCached(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"getent group admin": "admin:x:2000:\n"}}
	res := fakeResolver{"alice": {UID: 3001, GID: 3001, Groups: []uint32{3001, 2000}}}
	a := newTestAdmin(t, r, res)

	if ok, _ := a.IsAdmin(context.Background(), "alice"); !ok {
		t.Fatal("alice should start as an admin")
	}
	res["alice"] = helperpool.Creds{UID: 3001, GID: 3001, Groups: []uint32{3001}}
	if ok, _ := a.IsAdmin(context.Background(), "alice"); ok {
		t.Error("alice is still an admin after losing the group; the check is cached")
	}
}

// Verbatim `usersync export --format csv` output, copied from a running
// container rather than written from memory. The empty uid_number on a group
// row is the whole reason this is a fixture and not a sentence: a parser that
// reads the group's number out of column 2 finds nothing, drops every group,
// and silently reports that nobody is on a team.
const exportCSV = `type,name,uid_number,gid_number,unix_home_directory,login_shell
group,team-a,,10001,,
user,alice,3001,3001,/research/home/alice,/usr/sbin/nologin
user,bob,3002,3002,/research/home/bob,/usr/sbin/nologin
`

func inventoryFixture() (*fakeRunner, fakeResolver) {
	r := &fakeRunner{out: map[string]string{
		"getent group admin":           "admin:x:2000:\n",
		"usersync export --format csv": exportCSV,
		"pdbedit -L -v":                "Unix username:        alice\nAccount Flags:        [U          ]\n\nUnix username:        bob\nAccount Flags:        [DU         ]\n",
	}, err: map[string]error{}}
	res := fakeResolver{
		"alice": {UID: 3001, GID: 3001, Groups: []uint32{3001, 10001}},
		"bob":   {UID: 3002, GID: 3002, Groups: []uint32{3002}},
	}
	return r, res
}

func TestInventoryReportsAccountsGroupsAndSMBState(t *testing.T) {
	r, res := inventoryFixture()
	inv, err := newTestAdmin(t, r, res).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(inv.Users) != 2 {
		t.Fatalf("users = %d, want 2", len(inv.Users))
	}
	alice, bob := inv.Users[0], inv.Users[1]
	if alice.Name != "alice" || alice.UID != 3001 || alice.Home != "/research/home/alice" {
		t.Errorf("alice = %+v", alice)
	}
	// The UPG must not show up as a team; it is an implementation detail of the
	// account, not a group anyone joined.
	if len(alice.Groups) != 1 || alice.Groups[0] != "team-a" {
		t.Errorf("alice groups = %v, want [team-a]", alice.Groups)
	}
	if alice.SMB == nil || !alice.SMB.Enabled {
		t.Errorf("alice SMB = %+v, want enabled", alice.SMB)
	}
	if bob.SMB == nil || bob.SMB.Enabled {
		t.Errorf("bob SMB = %+v, want disabled (the D flag)", bob.SMB)
	}

	if len(inv.Groups) != 1 || inv.Groups[0].Name != "team-a" {
		t.Fatalf("groups = %+v", inv.Groups)
	}
	if len(inv.Groups[0].Members) != 1 || inv.Groups[0].Members[0] != "alice" {
		t.Errorf("team-a members = %v, want [alice]", inv.Groups[0].Members)
	}
}

// pdbedit being unavailable must read as "unknown", not as "no SMB account" --
// otherwise the page shows everyone locked out and sends someone to fix a
// problem that does not exist.
func TestInventoryDistinguishesUnknownSMBFromAbsent(t *testing.T) {
	r, res := inventoryFixture()
	r.err["pdbedit -L -v"] = errors.New("exec: pdbedit: not found")

	inv, err := newTestAdmin(t, r, res).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range inv.Users {
		if u.SMB != nil {
			t.Errorf("%s SMB = %+v, want nil when Samba could not be asked", u.Name, u.SMB)
		}
	}
	if len(inv.Warnings) == 0 {
		t.Error("no warning explaining why SMB state is missing")
	}
}

func TestSetSMBEnabledRunsSmbpasswd(t *testing.T) {
	r, res := inventoryFixture()
	a := newTestAdmin(t, r, res)

	if err := a.SetSMBEnabled(context.Background(), "bob", true); err != nil {
		t.Fatal(err)
	}
	if !contains(r.calls, "smbpasswd -e bob") {
		t.Errorf("calls = %v, want an `smbpasswd -e bob`", r.calls)
	}
}

// The admin group must not be a general-purpose Samba console. Only accounts
// this server manages are addressable.
func TestOpsRefuseUnmanagedNames(t *testing.T) {
	r, res := inventoryFixture()
	a := newTestAdmin(t, r, res)

	for _, name := range []string{"root", "nobody", "alice/../root", "alice bob", ""} {
		if err := a.SetSMBEnabled(context.Background(), name, false); !errors.Is(err, ErrUnknownUser) {
			t.Errorf("SetSMBEnabled(%q) = %v, want ErrUnknownUser", name, err)
		}
		if err := a.SetSMBPassword(context.Background(), name, "pw"); !errors.Is(err, ErrUnknownUser) {
			t.Errorf("SetSMBPassword(%q) = %v, want ErrUnknownUser", name, err)
		}
	}
}

// The password goes on stdin. An argv is world-readable through /proc.
func TestSetSMBPasswordSendsSecretOnStdinOnly(t *testing.T) {
	r, res := inventoryFixture()
	a := newTestAdmin(t, r, res)

	const pw = "correct horse battery"
	if err := a.SetSMBPassword(context.Background(), "alice", pw); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, pw) {
			t.Fatalf("password appeared in a command line: %q", c)
		}
	}
	found := false
	for _, in := range r.stdin {
		if in == pw+"\n"+pw+"\n" {
			found = true
		}
	}
	if !found {
		t.Errorf("password was not written twice on stdin; got %q", r.stdin)
	}
}

// A newline would make smbpasswd store only the prefix, so the account would
// end up with a password nobody was told.
func TestSetSMBPasswordRejectsNewlineAndEmpty(t *testing.T) {
	r, res := inventoryFixture()
	a := newTestAdmin(t, r, res)

	for _, pw := range []string{"", "good\nevil"} {
		if err := a.SetSMBPassword(context.Background(), "alice", pw); err == nil {
			t.Errorf("SetSMBPassword(%q) was accepted", pw)
		}
	}
}

// The two ways usersync can fail to answer are different facts about the
// deployment and must not collapse into one. "Not installed" is how the local
// stack runs and needs no attention; "installed and failed" does.
func TestAuditSeparatesNotInstalledFromFailed(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		r, res := inventoryFixture()
		r.err["usersync audit --json"] = fmt.Errorf("usersync audit --json: %w", &exec.Error{Name: "usersync", Err: exec.ErrNotFound})

		rep, err := newTestAdmin(t, r, res).Audit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if rep.Available {
			t.Error("Available = true with no usersync on the system")
		}
		if rep.Error != "" {
			t.Errorf("Error = %q; a tool this deployment does not use is not a failure to report", rep.Error)
		}
	})

	t.Run("installed but failed", func(t *testing.T) {
		r, res := inventoryFixture()
		r.err["usersync audit --json"] = errors.New("usersync audit --json: exit status 2: no roster")

		rep, err := newTestAdmin(t, r, res).Audit(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Available {
			t.Error("Available = false for a tool that ran")
		}
		if rep.Error == "" {
			t.Error("Error is empty; an audit that could not run must say so")
		}
	})
}

// Same distinction on the inventory: falling back to NSS is worth stating, but
// only the unexpected half is a warning.
func TestInventorySourceReflectsWhereTheListCameFrom(t *testing.T) {
	t.Run("usersync answered", func(t *testing.T) {
		r, res := inventoryFixture()
		inv, err := newTestAdmin(t, r, res).Inventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inv.Source != SourceUsersync {
			t.Errorf("Source = %q, want %q", inv.Source, SourceUsersync)
		}
	})

	t.Run("not installed falls back quietly", func(t *testing.T) {
		r, res := inventoryFixture()
		r.err["usersync export --format csv"] = fmt.Errorf("wrapped: %w", &exec.Error{Name: "usersync", Err: exec.ErrNotFound})
		r.out["getent passwd"] = "alice:x:3001:3001:Alice:/homes/alice:/usr/sbin/nologin\nroot:x:0:0:root:/root:/bin/sh\n"
		r.out["getent group"] = "team-a:x:10001:\nadmin:x:2000:alice\n"

		inv, err := newTestAdmin(t, r, res).Inventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inv.Source != SourceNSS {
			t.Errorf("Source = %q, want %q", inv.Source, SourceNSS)
		}
		for _, w := range inv.Warnings {
			if strings.Contains(w, "usersync") {
				t.Errorf("warned about a tool this deployment does not use: %q", w)
			}
		}
		// The band filter is what keeps root and the operator group off a page
		// about researchers.
		if len(inv.Users) != 1 || inv.Users[0].Name != "alice" {
			t.Errorf("users = %+v, want just alice (root is below the managed floor)", inv.Users)
		}
		if len(inv.Groups) != 1 || inv.Groups[0].Name != "team-a" {
			t.Errorf("groups = %+v, want just team-a (admin gid 2000 is below the band)", inv.Groups)
		}
	})

	t.Run("installed but failed is a warning", func(t *testing.T) {
		r, res := inventoryFixture()
		r.err["usersync export --format csv"] = errors.New("exit status 1: bad roster")

		inv, err := newTestAdmin(t, r, res).Inventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inv.Source != SourceNSS {
			t.Errorf("Source = %q, want %q", inv.Source, SourceNSS)
		}
		found := false
		for _, w := range inv.Warnings {
			if strings.Contains(w, "usersync") {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings = %v; a usersync that exists and failed must be surfaced", inv.Warnings)
		}
	})
}

func TestAuditParsesFindingsFromANonZeroExit(t *testing.T) {
	r, res := inventoryFixture()
	r.out["usersync audit --json"] = `{"findings":[{"kind":"user","name":"skim","code":"missing","want":3001}]}`
	r.err["usersync audit --json"] = errors.New("exit status 1")

	rep, err := newTestAdmin(t, r, res).Audit(context.Background())
	if err != nil {
		t.Fatalf("a non-zero exit is how audit reports drift, not a failure: %v", err)
	}
	if rep.OK {
		t.Error("OK = true with a finding present")
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Name != "skim" {
		t.Errorf("findings = %+v", rep.Findings)
	}
}

func TestUsageParsesZFSUserspace(t *testing.T) {
	r, res := inventoryFixture()
	r.out["zfs userspace -Hpn -o name,used /srv/data"] = "3001\t1048576\n3002\t2097152\n"
	a := newTestAdmin(t, r, res)

	a.RefreshUsage(context.Background())

	rep := a.Usage()
	if rep.Source != "zfs" {
		t.Errorf("source = %q, want zfs", rep.Source)
	}
	if rep.ByUID["3001"] != 1048576 || rep.ByUID["3002"] != 2097152 {
		t.Errorf("by_uid = %v", rep.ByUID)
	}
}

// Without ZFS the numbers come from a traversal, and they must still be
// attributed by uid -- a name that no longer resolves still owns its bytes.
func TestUsageFallsBackToATraversal(t *testing.T) {
	r, res := inventoryFixture()
	r.err["zfs userspace -Hpn -o name,used /srv/data"] = errors.New("exec: zfs: not found")
	// the runner key is TrimSpace'd, so the trailing newline in -printf is gone
	r.out["find /srv/data -xdev -type f -printf %U %b"] = "3001 2\n3001 4\n3002 8\n"
	a := newTestAdmin(t, r, res)

	a.RefreshUsage(context.Background())

	rep := a.Usage()
	if rep.Source != "du" {
		t.Errorf("source = %q, want du", rep.Source)
	}
	if got := rep.ByUID["3001"]; got != 3072 { // (2+4) blocks * 512
		t.Errorf("uid 3001 = %d, want 3072", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

const declJSON = `{
  "groups": [
    {"name":"team-a","gid":10001,"owners":["alice"]},
    {"name":"team-b","gid":10002,"owners":[]}
  ],
  "users": [
    {"name":"alice","uid":3001,"groups":["team-a"],"status":"active"},
    {"name":"bob","uid":3002,"groups":[],"status":"active"},
    {"name":"carol","uid":3003,"groups":["team-b"],"status":"active"}
  ]
}`

func teamFixture() (*fakeRunner, fakeResolver) {
	r, res := inventoryFixture()
	r.out["usersync roster"] = declJSON
	r.out["usersync member add bob team-a"] = ""
	r.out["usersync member remove alice team-a"] = ""
	r.out["usersync apply"] = ""
	// carol is neither an admin nor an owner.
	res["carol"] = helperpool.Creds{UID: 3003, GID: 3003, Groups: []uint32{3003, 10002}}
	return r, res
}

// PublicFolders reports exactly the groups whose anonymous level is read or
// write, sorted, with the write flag set only for the writable ones.
func TestPublicFolders(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"usersync roster": `{"groups":[
			{"name":"secret","gid":10001,"owners":[],"readers":[],"anonymous":"none"},
			{"name":"dropbox","gid":10004,"owners":[],"readers":[],"anonymous":"write"},
			{"name":"handbook","gid":10003,"owners":[],"readers":[],"anonymous":"read"},
			{"name":"plain","gid":10002,"owners":[],"readers":[]}
		],"users":[]}`,
	}}
	a := newTestAdmin(t, r, fakeResolver{})

	got, err := a.PublicFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by name; only handbook (read) and dropbox (write) are public.
	if len(got) != 2 || got[0].Name != "dropbox" || !got[0].Write || got[1].Name != "handbook" || got[1].Write {
		t.Fatalf("PublicFolders = %+v, want [dropbox(write), handbook(read)]", got)
	}
}

// Owner and admin are separate axes. alice owns team-a AND is in the admin
// group in this fixture; bob is an admin in no sense; carol is neither.
func TestOwnedTeamsComesFromTheDeclaration(t *testing.T) {
	r, res := teamFixture()
	a := newTestAdmin(t, r, res)

	for user, want := range map[string][]string{
		"alice": {"team-a"},
		"bob":   {},
		"carol": {},
	} {
		got, err := a.OwnedTeams(context.Background(), user)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) || (len(want) > 0 && got[0] != want[0]) {
			t.Errorf("OwnedTeams(%q) = %v, want %v", user, got, want)
		}
	}
}

// An owner may change their own team and nothing else. This is the boundary the
// whole feature rests on, so both halves are asserted.
func TestOwnerMayManageOnlyTheirOwnTeam(t *testing.T) {
	r, res := teamFixture()
	// alice owns team-a but is NOT an admin here, so the admin path cannot be
	// what lets her through.
	res["alice"] = helperpool.Creds{UID: 3001, GID: 3001, Groups: []uint32{3001, 10001}}
	a := newTestAdmin(t, r, res)

	if err := a.SetTeamMembership(context.Background(), "alice", "team-a", "bob", true); err != nil {
		t.Fatalf("owner refused on their own team: %v", err)
	}
	if !contains(r.calls, "usersync member add bob team-a") {
		t.Errorf("calls = %v, want the member edit", r.calls)
	}
	// And it converged, rather than leaving the change to the next restart.
	if !contains(r.calls, "usersync apply") {
		t.Errorf("calls = %v, want an apply after the edit", r.calls)
	}

	err := a.SetTeamMembership(context.Background(), "alice", "team-b", "bob", true)
	if !errors.Is(err, ErrNotOwner) {
		t.Errorf("alice on team-b = %v, want ErrNotOwner", err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "team-b") {
			t.Errorf("the refusal still ran %q", c)
		}
	}
}

// Someone who owns nothing is refused everywhere.
func TestNonOwnerIsRefused(t *testing.T) {
	r, res := teamFixture()
	a := newTestAdmin(t, r, res)

	for _, team := range []string{"team-a", "team-b"} {
		if err := a.SetTeamMembership(context.Background(), "carol", team, "bob", true); !errors.Is(err, ErrNotOwner) {
			t.Errorf("carol on %s = %v, want ErrNotOwner", team, err)
		}
	}
}

// An admin may manage any team: withholding this one operation from the person
// who can already reset everyone's password is a distinction without a
// difference.
func TestAdminMayManageAnyTeam(t *testing.T) {
	r, res := teamFixture()
	// gid 2000 is the admin group (see the getent fixture). bob owns no team.
	res["bob"] = helperpool.Creds{UID: 3002, GID: 3002, Groups: []uint32{3002, 2000}}
	a := newTestAdmin(t, r, res)

	if owned, _ := a.OwnedTeams(context.Background(), "bob"); len(owned) != 0 {
		t.Fatalf("bob owns %v; the fixture no longer isolates the admin path", owned)
	}
	for _, team := range []string{"team-a", "team-b"} {
		if ok, err := a.MayManageTeam(context.Background(), "bob", team); err != nil || !ok {
			t.Errorf("admin on %s = %v (err %v), want true", team, ok, err)
		}
	}
}

// The declaration is read from the ROSTER, never from gshadow. Otherwise anyone
// who could write gshadow could grant themselves the delegation.
func TestOwnershipIsReadFromTheRosterNotTheSystem(t *testing.T) {
	r, res := teamFixture()
	a := newTestAdmin(t, r, res)

	if _, err := a.OwnedTeams(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "gshadow") || strings.Contains(c, "gpasswd") {
			t.Errorf("ownership was decided by %q", c)
		}
	}
	if !contains(r.calls, "usersync roster") {
		t.Errorf("calls = %v, want `usersync roster`", r.calls)
	}
}

// A name the roster does not declare is refused before anything runs.
func TestUnknownTargetUserIsRefused(t *testing.T) {
	r, res := teamFixture()
	a := newTestAdmin(t, r, res)

	if err := a.SetTeamMembership(context.Background(), "alice", "team-a", "nobody", true); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("target nobody = %v, want ErrUnknownUser", err)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "usersync member") {
			t.Errorf("the refusal still ran %q", c)
		}
	}
}

// A failed apply must not be reported as success, and must not undo the roster
// edit: the declaration now says the right thing, so the recovery is another
// apply rather than putting it back to something nobody asked for.
func TestApplyFailureIsReportedAndNotRolledBack(t *testing.T) {
	r, res := teamFixture()
	r.err["usersync apply"] = errors.New("exit status 1: useradd busy")
	a := newTestAdmin(t, r, res)

	err := a.SetTeamMembership(context.Background(), "alice", "team-a", "bob", true)
	if err == nil {
		t.Fatal("a failed apply was reported as success")
	}
	if !strings.Contains(err.Error(), "usersync apply") {
		t.Errorf("error = %v, want it to name the recovery", err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "member remove bob team-a") {
			t.Error("the edit was rolled back")
		}
	}
}
