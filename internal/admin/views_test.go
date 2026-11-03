package admin

import (
	"context"
	"testing"
)

// The roster the routing tests reason about.
//
//	perception          alice is a member
//	simulation          bob is a member; simulation-readers reads it
//	simulation-readers  alice is a member  -> alice reads simulation through it
//	notice              read by `everyone`, which both are in
//	both-ro / alt-ro    two of alice's groups read `shared`; the choice must be stable
const routeRoster = `{"schema":2,"paths":{"home":"/srv/data/homes","groups":"/srv/data/teams","views":"/srv/data/views"},"groups":[
	{"name":"perception","gid":10001,"members":["alice"]},
	{"name":"simulation","gid":10017,"readers":["simulation-readers"],"members":["bob"]},
	{"name":"simulation-readers","gid":10020,"members":["alice"],
	 "views":[{"team":"simulation","path":"/srv/data/views/simulation-readers/simulation"}]},
	{"name":"everyone","gid":10050,"all":true,"members":["alice","bob"],
	 "views":[{"team":"notice","path":"/srv/data/views/everyone/notice"}]},
	{"name":"notice","gid":10051,"readers":["everyone"]},
	{"name":"shared","gid":10060,"readers":["both-ro","alt-ro"],"members":["bob"]},
	{"name":"alt-ro","gid":10061,"members":["alice"],
	 "views":[{"team":"shared","path":"/srv/data/views/alt-ro/shared"}]},
	{"name":"both-ro","gid":10062,"members":["alice"],
	 "views":[{"team":"shared","path":"/srv/data/views/both-ro/shared"}]}
],"users":[{"name":"alice","uid":3001},{"name":"bob","uid":3002}]}`

func routeAdmin(t *testing.T) *Admin {
	t.Helper()
	return newTestAdmin(t, &fakeRunner{out: map[string]string{"usersync roster": routeRoster}}, fakeResolver{})
}

// A reader's request at a team folder goes to the read-only view usersync
// declared; everything else is left exactly as it was asked for.
func TestReroutePath(t *testing.T) {
	a := routeAdmin(t)
	ctx := context.Background()

	for _, tc := range []struct {
		what, user, in, want string
	}{
		{"a reader is sent to the view",
			"alice", "teams/simulation", "views/simulation-readers/simulation"},
		{"and so is everything under it",
			"alice", "teams/simulation/runs/2026/a.zarr", "views/simulation-readers/simulation/runs/2026/a.zarr"},
		{"a member keeps the writable path",
			"bob", "teams/simulation", "teams/simulation"},
		{"a member of the team is never routed, even to a team they also read",
			"alice", "teams/perception/notes.md", "teams/perception/notes.md"},
		{"somebody with no claim is left to be refused",
			"bob", "teams/perception", "teams/perception"},
		{"an `all` reader group routes like any other",
			"bob", "teams/notice/memo.pdf", "views/everyone/notice/memo.pdf"},
		{"the teams root is not a team",
			"alice", "teams", "teams"},
		{"a home is not a team",
			"alice", "homes/alice/x", "homes/alice/x"},
		{"the view's own path is already where it should go",
			"alice", "views/simulation-readers/simulation", "views/simulation-readers/simulation"},
	} {
		if got := a.ReroutePath(ctx, tc.user, tc.in); got != tc.want {
			t.Errorf("%s: ReroutePath(%q, %q) = %q; want %q", tc.what, tc.user, tc.in, got, tc.want)
		}
	}
}

// Rerouting must be a function of the request, not of the order the roster
// happened to list things in: the same path asked for twice has to resolve to
// the same place, or two identical requests behave differently.
func TestReroutePathIsStableWithSeveralReaderGroups(t *testing.T) {
	a := routeAdmin(t)
	ctx := context.Background()

	first := a.ReroutePath(ctx, "alice", "teams/shared/f.txt")
	if want := "views/alt-ro/shared/f.txt"; first != want {
		t.Fatalf("ReroutePath = %q; want %q (the sorted first of alice's reader groups)", first, want)
	}
	for range 5 {
		if got := a.ReroutePath(ctx, "alice", "teams/shared/f.txt"); got != first {
			t.Fatalf("ReroutePath is not stable: %q then %q", first, got)
		}
	}
}

// Routing is applied per filesystem operation, so it must not shell out to
// `usersync roster` per filesystem operation.
func TestReroutePathReusesTheRoster(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"usersync roster": routeRoster}}
	a := newTestAdmin(t, r, fakeResolver{})
	ctx := context.Background()

	for range 20 {
		a.ReroutePath(ctx, "alice", "teams/simulation/f.txt")
	}
	var reads int
	for _, c := range r.calls {
		if c == "usersync roster" {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("read the roster %d times for 20 operations; want 1", reads)
	}
}

// A roster that cannot be read leaves the path alone. The reader then lands on
// the team's own folder and is refused by the kernel — which is the direction to
// fail in, because the alternative would be routing somebody somewhere on a
// guess.
func TestReroutePathLeavesThePathAloneWhenTheRosterIsUnreadable(t *testing.T) {
	r := &fakeRunner{out: map[string]string{}} // no `usersync roster` output at all
	a := newTestAdmin(t, r, fakeResolver{})

	const p = "teams/simulation/f.txt"
	if got := a.ReroutePath(context.Background(), "alice", p); got != p {
		t.Errorf("ReroutePath = %q; want the path unchanged (%q)", got, p)
	}
}

// usersync says where each view is (`views`), and darak opens that path instead
// of building one: a view outside the data root, or none declared, is no route.
const routeRosterViews = `{"schema":2,"paths":{"home":"/srv/data/homes","groups":"/srv/data/teams","views":"/srv/data/views"},"groups":[
	{"name":"perception","gid":10001,"members":["alice"]},
	{"name":"simulation","gid":10017,"readers":["simulation-readers"],"members":["bob"]},
	{"name":"simulation-readers","gid":10020,"members":["alice"],
	 "views":[{"team":"simulation","path":"/srv/data/views/simulation-readers/simulation"}]},
	{"name":"outside","gid":10030,"readers":["far-ro"],"members":["bob"]},
	{"name":"far-ro","gid":10031,"members":["alice"],
	 "views":[{"team":"outside","path":"/elsewhere/far-ro/outside"}]},
	{"name":"undeclared","gid":10040,"readers":["no-views"],"members":["bob"]},
	{"name":"no-views","gid":10041,"members":["alice"]}
],"users":[{"name":"alice","uid":3001},{"name":"bob","uid":3002}]}`

func TestReroutePathFollowsTheDeclaredView(t *testing.T) {
	a := newTestAdmin(t, &fakeRunner{out: map[string]string{"usersync roster": routeRosterViews}}, fakeResolver{})
	ctx := context.Background()

	for _, tc := range []struct {
		what, user, in, want string
	}{
		{"a reader goes where usersync says the view is",
			"alice", "teams/simulation/runs/a.zarr", "views/simulation-readers/simulation/runs/a.zarr"},
		{"a member keeps the writable path",
			"bob", "teams/simulation/runs/a.zarr", "teams/simulation/runs/a.zarr"},
		{"a view outside the data root has no route, so the reader is refused",
			"alice", "teams/outside/x", "teams/outside/x"},
		{"a reader with no declared view is not routed by a rule of ours",
			"alice", "teams/undeclared/x", "teams/undeclared/x"},
	} {
		if got := a.ReroutePath(ctx, tc.user, tc.in); got != tc.want {
			t.Errorf("%s: ReroutePath(%q, %q) = %q; want %q", tc.what, tc.user, tc.in, got, tc.want)
		}
	}
}

// A schema this code does not know is refused — newer, or older than `views` —
// and refusal leaves every path unrouted: a reader is turned away rather than
// sent somewhere guessed.
func TestDeclarationRefusesAnUnknownSchema(t *testing.T) {
	for _, roster := range []string{
		`{"schema":3,"groups":[],"users":[]}`,
		`{"groups":[],"users":[]}`, // schema 0: predates views
	} {
		a := newTestAdmin(t, &fakeRunner{out: map[string]string{"usersync roster": roster}}, fakeResolver{})
		if _, err := a.Declaration(context.Background()); err == nil {
			t.Errorf("Declaration accepted %s", roster)
		}
		if got := a.ReroutePath(context.Background(), "alice", "teams/simulation/x"); got != "teams/simulation/x" {
			t.Errorf("ReroutePath = %q; want the path unchanged", got)
		}
	}
	// schema 1 has the same shape as 2.
	one := `{"schema":1,"groups":[],"users":[]}`
	a := newTestAdmin(t, &fakeRunner{out: map[string]string{"usersync roster": one}}, fakeResolver{})
	if _, err := a.Declaration(context.Background()); err != nil {
		t.Errorf("schema 1 refused: %v", err)
	}
}
