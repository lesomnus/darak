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
const routeRoster = `{"groups":[
	{"name":"perception","gid":10001,"members":["alice"]},
	{"name":"simulation","gid":10017,"readers":["simulation-readers"],"members":["bob"]},
	{"name":"simulation-readers","gid":10020,"members":["alice"]},
	{"name":"everyone","gid":10050,"all":true,"members":["alice","bob"]},
	{"name":"notice","gid":10051,"readers":["everyone"]},
	{"name":"shared","gid":10060,"readers":["both-ro","alt-ro"],"members":["bob"]},
	{"name":"alt-ro","gid":10061,"members":["alice"]},
	{"name":"both-ro","gid":10062,"members":["alice"]}
],"users":[{"name":"alice","uid":3001},{"name":"bob","uid":3002}]}`

func routeAdmin(t *testing.T) *Admin {
	t.Helper()
	return newTestAdmin(t, &fakeRunner{out: map[string]string{"usersync roster": routeRoster}}, fakeResolver{})
}

// A reader's request at a team folder goes to the read-only view in their own
// group's folder; everything else is left exactly as it was asked for.
func TestReroutePath(t *testing.T) {
	a := routeAdmin(t)
	ctx := context.Background()

	for _, tc := range []struct {
		what, user, in, want string
	}{
		{"a reader is sent to the view",
			"alice", "teams/simulation", "teams/simulation-readers/simulation"},
		{"and so is everything under it",
			"alice", "teams/simulation/runs/2026/a.zarr", "teams/simulation-readers/simulation/runs/2026/a.zarr"},
		{"a member keeps the writable path",
			"bob", "teams/simulation", "teams/simulation"},
		{"a member of the team is never routed, even to a team they also read",
			"alice", "teams/perception/notes.md", "teams/perception/notes.md"},
		{"somebody with no claim is left to be refused",
			"bob", "teams/perception", "teams/perception"},
		{"an `all` reader group routes like any other",
			"bob", "teams/notice/memo.pdf", "teams/everyone/notice/memo.pdf"},
		{"the teams root is not a team",
			"alice", "teams", "teams"},
		{"a home is not a team",
			"alice", "homes/alice/x", "homes/alice/x"},
		{"the view's own path is already where it should go",
			"alice", "teams/simulation-readers/simulation", "teams/simulation-readers/simulation"},
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
	if want := "teams/alt-ro/shared/f.txt"; first != want {
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
