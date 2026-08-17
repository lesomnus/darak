package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lesomnus/darak/internal/admin"
	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/control"
	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/vfs"
)

// scriptRunner answers the backend tools from a table.
type scriptRunner struct {
	out   map[string]string
	err   map[string]error
	calls []string
}

func (s *scriptRunner) Run(ctx context.Context, stdin, name string, args ...string) (string, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	s.calls = append(s.calls, key)
	return s.out[key], s.err[key]
}

type mapResolver map[string]helperpool.Creds

func (m mapResolver) Resolve(ctx context.Context, user string) (helperpool.Creds, error) {
	c, ok := m[user]
	if !ok {
		return helperpool.Creds{}, http.ErrNoCookie
	}
	return c, nil
}

// adminHarness is newHarness plus an Admin wired to scripted backends. alice is
// in the admin group; bob is not.
func adminHarness(t *testing.T) (*harness, *scriptRunner) {
	t.Helper()
	return adminHarnessWith(t, fakeAuth{ok: true})
}

// adminHarnessWith is adminHarness with a chosen authenticator. Whether the
// credential store accepts a password is what tells the initial-password route
// if it is still the real one.
func adminHarnessWith(t *testing.T, a auth.Authenticator) (*harness, *scriptRunner) {
	t.Helper()
	base := newHarness(t, a)

	run := &scriptRunner{
		out: map[string]string{
			"getent group admin":           "admin:x:2000:\n",
			"usersync export --format csv": "type,name,uid_number,gid_number,unix_home_directory,login_shell\nuser,alice,3001,3001,/homes/alice,/usr/sbin/nologin\nuser,bob,3002,3002,/homes/bob,/usr/sbin/nologin\n",
			"pdbedit -L -v":                "Unix username:\talice\nAccount Flags:\t[U]\n\nUnix username:\tbob\nAccount Flags:\t[U]\n",
			"usersync audit --json":        `{"findings":[]}`,
			"usersync roster": `{"groups":[{"name":"team-a","gid":10001,"owners":["bob"],"members":["bob"]},` +
				`{"name":"team-b","gid":10002,"owners":[]}],` +
				`"users":[{"name":"alice","uid":3001,"status":"active"},` +
				`{"name":"bob","uid":3002,"status":"active"},` +
				`{"name":"carol","uid":3003,"status":"active"}]}`,
			"usersync member add alice team-a":    "",
			"usersync member add carol team-a":    "",
			"usersync member add carol team-b":    "",
			"usersync member remove alice team-a": "",
			"usersync apply":                      "",
			"usersync passwd bob":                 "Hd-ABCDEFGHIJKLMNOP\n",
			"usersync passwd alice":               "Hd-QRSTUVWXYZ234567\n",
		},
		err: map[string]error{},
	}
	adm, err := admin.New(admin.Config{
		Root:       base.root,
		Runner:     run,
		Controller: control.Local(run, "usersync"),
		Resolver: mapResolver{
			// alice is an ADMIN (gid 2000) and owns no team.
			"alice": {UID: 3001, GID: 3001, Groups: []uint32{3001, 2000}},
			// bob OWNS team-a (see the roster fixture) and is not an admin. The
			// two permissions are separate axes and the tests need both cases.
			"bob": {UID: 3002, GID: 3002, Groups: []uint32{3002, 10001}},
			// carol is neither, which is what most people are.
			"carol": {UID: 3003, GID: 3003, Groups: []uint32{3003}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{
		FS:    &vfs.FS{Pool: base.doer},
		Auth:  a,
		Admin: adm,
	})
	if err != nil {
		t.Fatal(err)
	}
	base.srv = s.Handler()
	return base, run
}

// adminRoutes is every route behind the gate, with a body where one is needed.
var adminRoutes = []struct{ method, path, body string }{
	{"GET", "/api/admin/users", ""},
	{"GET", "/api/admin/disk", ""},
	{"GET", "/api/admin/audit", ""},
	{"POST", "/api/admin/users/bob/smb", `{"enabled":false}`},
	{"POST", "/api/admin/users/bob/password", `{"password":"hunter2"}`},
	{"GET", "/api/admin/users/bob/initial-password", ""},
}

// The gate is on every route, not just the page. A signed-in non-admin calling
// the API directly is the case that matters -- the UI hiding a button is a
// rendering choice, not a permission.
func TestAdminRoutesAreClosedToNonAdmins(t *testing.T) {
	h, run := adminHarness(t)
	bob := h.login("bob")

	for _, rt := range adminRoutes {
		rec := h.do(rt.method, rt.path, strings.NewReader(rt.body), bob)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as bob = %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	// And nothing was attempted on the way to refusing.
	for _, c := range run.calls {
		if strings.HasPrefix(c, "smbpasswd") {
			t.Errorf("a non-admin request reached smbpasswd: %q", c)
		}
	}
}

func TestAdminRoutesRequireASession(t *testing.T) {
	h, _ := adminHarness(t)

	for _, rt := range adminRoutes {
		rec := h.do(rt.method, rt.path, strings.NewReader(rt.body), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
}

// 404 rather than 403: there is nothing to gain from telling a signed-in
// non-admin that an operator API exists here.
func TestAdminRefusalDoesNotAdvertiseTheAPI(t *testing.T) {
	h, _ := adminHarness(t)
	bob := h.login("bob")

	rec := h.do("GET", "/api/admin/users", nil, bob)
	if body := rec.Body.String(); strings.Contains(strings.ToLower(body), "admin") {
		t.Errorf("the refusal names the admin surface: %q", body)
	}
}

func TestAdminCanReadTheInventory(t *testing.T) {
	h, _ := adminHarness(t)
	alice := h.login("alice")

	rec := h.do("GET", "/api/admin/users", nil, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/users as alice = %d %s", rec.Code, rec.Body)
	}
	var inv admin.Inventory
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if len(inv.Users) != 2 {
		t.Fatalf("users = %d, want 2", len(inv.Users))
	}
}

func TestAdminCanSuspendAnotherUser(t *testing.T) {
	h, run := adminHarness(t)
	alice := h.login("alice")

	rec := h.do("POST", "/api/admin/users/bob/smb", strings.NewReader(`{"enabled":false}`), alice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("suspend bob = %d %s", rec.Code, rec.Body)
	}
	found := false
	for _, c := range run.calls {
		if c == "smbpasswd -d bob" {
			found = true
		}
	}
	if !found {
		t.Errorf("calls = %v, want `smbpasswd -d bob`", run.calls)
	}
}

// Locking yourself out is an accident, not an operation.
func TestAdminCannotDisableTheirOwnAccount(t *testing.T) {
	h, run := adminHarness(t)
	alice := h.login("alice")

	rec := h.do("POST", "/api/admin/users/alice/smb", strings.NewReader(`{"enabled":false}`), alice)
	if rec.Code != http.StatusConflict {
		t.Fatalf("self-disable = %d %s, want 409", rec.Code, rec.Body)
	}
	for _, c := range run.calls {
		if strings.Contains(c, "smbpasswd -d alice") {
			t.Fatal("the refusal still ran smbpasswd")
		}
	}
	// Re-enabling yourself is fine; it is the lockout that is refused.
	if rec := h.do("POST", "/api/admin/users/alice/smb", strings.NewReader(`{"enabled":true}`), alice); rec.Code != http.StatusNoContent {
		t.Errorf("self-enable = %d %s, want 204", rec.Code, rec.Body)
	}
}

// An unmanaged name must not be addressable even for an admin: this is a page
// for this server's accounts, not a Samba console.
func TestAdminCannotTouchUnmanagedAccounts(t *testing.T) {
	h, _ := adminHarness(t)
	alice := h.login("alice")

	for _, name := range []string{"root", "nobody"} {
		rec := h.do("POST", "/api/admin/users/"+name+"/smb", strings.NewReader(`{"enabled":false}`), alice)
		if rec.Code != http.StatusNotFound {
			t.Errorf("target %q = %d, want 404", name, rec.Code)
		}
	}
}

// Every signed-in user may ask; most get false. This is what lets the interface
// decide whether to show the link without leaking anything.
func TestAdminWhoamiIsOpenToEveryone(t *testing.T) {
	h, _ := adminHarness(t)

	for user, want := range map[string]bool{"alice": true, "bob": false} {
		rec := h.do("GET", "/api/admin/whoami", nil, h.login(user))
		if rec.Code != http.StatusOK {
			t.Fatalf("whoami as %s = %d", user, rec.Code)
		}
		var got struct {
			Admin bool   `json:"admin"`
			Group string `json:"group"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Admin != want {
			t.Errorf("whoami as %s = %v, want %v", user, got.Admin, want)
		}
		if got.Group != "admin" {
			t.Errorf("group = %q, want admin", got.Group)
		}
	}
}

// With no Admin configured the surface does not exist at all -- and it looks
// exactly like a caller who does not qualify, so nothing is disclosed.
func TestAdminSurfaceAbsentWhenNotConfigured(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	alice := h.login("alice")

	for _, rt := range adminRoutes {
		rec := h.do(rt.method, rt.path, strings.NewReader(rt.body), alice)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s with no Admin = %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	rec := h.do("GET", "/api/admin/whoami", nil, alice)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"admin":false`) {
		t.Errorf("whoami = %d %s, want 200 with admin false", rec.Code, rec.Body)
	}
}

var _ = auth.ErrUnavailable

// bob owns team-a in the harness but is NOT in the admin group. That is the
// point: the two permissions are separate axes, and the team routes must let an
// owner through without opening the operator page to them.
func TestOwnerReachesTeamRoutesButNotTheAdminPage(t *testing.T) {
	h, _ := adminHarness(t)
	bob := h.login("bob")

	rec := h.do("POST", "/api/teams/team-a/members", strings.NewReader(`{"user":"alice","member":true}`), bob)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner changing their own team = %d %s, want 204", rec.Code, rec.Body)
	}

	// ...and the operator page is still closed to him.
	for _, rt := range adminRoutes {
		if rec := h.do(rt.method, rt.path, strings.NewReader(rt.body), bob); rec.Code != http.StatusNotFound {
			t.Errorf("owner reached %s %s = %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
}

// An owner is refused on a team they do not own, with 404 rather than 403 —
// a signed-in stranger learns nothing about which teams exist.
func TestOwnerIsRefusedOnAnotherTeam(t *testing.T) {
	h, run := adminHarness(t)
	bob := h.login("bob")

	rec := h.do("POST", "/api/teams/team-b/members", strings.NewReader(`{"user":"alice","member":true}`), bob)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("owner on another team = %d %s, want 404", rec.Code, rec.Body)
	}
	for _, c := range run.calls {
		if strings.Contains(c, "team-b") && strings.HasPrefix(c, "usersync member") {
			t.Errorf("the refusal still ran %q", c)
		}
	}
}

// carol is neither an admin nor an owner -- what most people are.
func TestNonOwnerIsRefusedOnTeamRoutes(t *testing.T) {
	h, _ := adminHarness(t)
	carol := h.login("carol")

	for _, team := range []string{"team-a", "team-b"} {
		rec := h.do("POST", "/api/teams/"+team+"/members", strings.NewReader(`{"user":"bob","member":true}`), carol)
		if rec.Code != http.StatusNotFound {
			t.Errorf("non-owner on %s = %d, want 404", team, rec.Code)
		}
	}
}

// An ADMIN may manage any team, including ones they do not own. alice owns
// nothing here and is in the admin group.
func TestAdminReachesAnyTeamRoute(t *testing.T) {
	h, _ := adminHarness(t)
	alice := h.login("alice")

	for _, team := range []string{"team-a", "team-b"} {
		rec := h.do("POST", "/api/teams/"+team+"/members", strings.NewReader(`{"user":"carol","member":true}`), alice)
		if rec.Code != http.StatusNoContent {
			t.Errorf("admin on %s = %d %s, want 204", team, rec.Code, rec.Body)
		}
	}
}

func TestTeamRoutesRequireASession(t *testing.T) {
	h, _ := adminHarness(t)

	rec := h.do("POST", "/api/teams/team-a/members", strings.NewReader(`{"user":"alice","member":true}`), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out = %d, want 401", rec.Code)
	}
}

// Every signed-in user may ask which teams they own; most own none.
func TestTeamWhoamiIsOpenToEveryone(t *testing.T) {
	h, _ := adminHarness(t)

	for user, want := range map[string]int{"bob": 1, "alice": 0, "carol": 0} {
		rec := h.do("GET", "/api/teams/whoami", nil, h.login(user))
		if rec.Code != http.StatusOK {
			t.Fatalf("whoami as %s = %d", user, rec.Code)
		}
		var got struct {
			Teams []string `json:"teams"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Teams) != want {
			t.Errorf("teams for %s = %v, want %d", user, got.Teams, want)
		}
	}
}

// The initial password is the one thing on this page that hands over a way in
// rather than changing one, so what it will and will not say is worth pinning.
func TestInitialPassword(t *testing.T) {
	h, run := adminHarness(t)
	alice := h.login("alice")

	rec := h.do("GET", "/api/admin/users/bob/initial-password", nil, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		StillInitial bool   `json:"still_initial"`
		Password     string `json:"password"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	// The authenticator accepts, so the derived value is still the real one.
	if !got.StillInitial || got.Password != "Hd-ABCDEFGHIJKLMNOP" {
		t.Errorf("got %+v", got)
	}
	// A credential must not be left in a proxy or the browser cache.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", cc)
	}
	if !containsCall(run.calls, "usersync passwd bob") {
		t.Errorf("usersync was not asked: %v", run.calls)
	}
}

// usersync prints the derived value forever, with no idea whether the person
// has since changed theirs. Handing that over would occasionally tell somebody
// their password is something it is not — which arrives as a broken login
// rather than as a stale note.
func TestInitialPasswordIsWithheldOnceChanged(t *testing.T) {
	// Accepts the sign-in but rejects the derived value, which is exactly the
	// state of somebody who has changed their password.
	h, _ := adminHarnessWith(t, authFunc(func(_, password string) (bool, error) {
		return !strings.HasPrefix(password, "Hd-"), nil
	}))
	alice := h.login("alice")

	rec := h.do("GET", "/api/admin/users/bob/initial-password", nil, alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["still_initial"] != false {
		t.Errorf("still_initial = %v; want false", got["still_initial"])
	}
	if _, leaked := got["password"]; leaked {
		t.Error("a password that no longer works was shown anyway")
	}
}

// "Cannot ask" must not become "here it is". An unverifiable value is one that
// may already be wrong.
func TestInitialPasswordIsWithheldWhenUnverifiable(t *testing.T) {
	h, _ := adminHarnessWith(t, authFunc(func(_, password string) (bool, error) {
		if strings.HasPrefix(password, "Hd-") {
			return false, auth.ErrUnavailable
		}
		return true, nil
	}))
	alice := h.login("alice")

	rec := h.do("GET", "/api/admin/users/bob/initial-password", nil, alice)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Hd-") {
		t.Error("the derived value leaked in the error")
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
