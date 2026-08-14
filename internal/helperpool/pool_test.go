package helperpool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/helper"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// fakeSpawner produces real, working helpers in this process — same protocol,
// same openat2 resolution — but without the privilege drop, which is what would
// otherwise force these tests to run as root. The uid separation itself is
// covered by internal/integration.
type fakeSpawner struct {
	t    *testing.T
	root string

	mu      sync.Mutex
	specs   []Spec
	helpers []*Helper
}

func newFakeSpawner(t *testing.T, root string) *fakeSpawner {
	s := &fakeSpawner{t: t, root: root}
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, h := range s.helpers {
			_ = h.Stop()
		}
	})
	return s
}

func (s *fakeSpawner) spawn(spec Spec) (*Helper, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	helperEnd := os.NewFile(uintptr(fds[0]), "helper")
	clientEnd := os.NewFile(uintptr(fds[1]), "client")

	h, err := helper.New(s.root, helperEnd)
	helperEnd.Close()
	if err != nil {
		clientEnd.Close()
		return nil, err
	}
	go func() { _ = h.Serve(); h.Close() }()

	conn, err := net.FileConn(clientEnd)
	clientEnd.Close()
	if err != nil {
		return nil, err
	}
	out := &Helper{Client: NewClient(conn.(*net.UnixConn)), creds: spec.Creds}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = append(s.specs, spec)
	s.helpers = append(s.helpers, out)
	return out, nil
}

func (s *fakeSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.specs)
}

// stopped reports whether the n-th spawned helper has been shut down.
func (s *fakeSpawner) stopped(n int) bool {
	s.mu.Lock()
	h := s.helpers[n]
	s.mu.Unlock()
	_, _, err := h.Do(context.Background(), &wire.Request{Op: wire.OpStat, Path: "."})
	return err != nil
}

// staticResolver answers with whatever it is currently set to.
type staticResolver struct {
	mu    sync.Mutex
	creds map[string]Creds
	calls int
}

func (r *staticResolver) set(user string, c Creds) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creds[user] = c
}

func (r *staticResolver) Resolve(_ context.Context, user string) (Creds, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	c, ok := r.creds[user]
	if !ok {
		return Creds{}, errors.New("no such user")
	}
	return c, nil
}

func newPool(t *testing.T, cfg Config) (*Pool, *fakeSpawner, *staticResolver) {
	t.Helper()
	root := t.TempDir()
	sp := newFakeSpawner(t, root)
	res := &staticResolver{creds: map[string]Creds{
		"alice": {UID: 3001, GID: 3001, Groups: []uint32{3001, 10001}},
		"bob":   {UID: 3002, GID: 3002, Groups: []uint32{3002, 10001}},
		"carol": {UID: 3003, GID: 3003, Groups: []uint32{3003}},
	}}
	cfg.Bin, cfg.Root = "unused", root
	cfg.Resolver = res
	cfg.spawn = sp.spawn

	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, sp, res
}

func statDot(t *testing.T, p *Pool, user string) {
	t.Helper()
	resp, _, err := p.Do(context.Background(), user, &wire.Request{Op: wire.OpStat, Path: "."})
	if err != nil {
		t.Fatalf("Do as %s: %v", user, err)
	}
	if !resp.OK() {
		t.Fatalf("Do as %s: errno %v", user, unix.Errno(resp.Errno))
	}
}

// waitStopped waits briefly for the n-th spawned helper's Stop to have run.
//
// retire() stops a helper in a goroutine (`go e.helper.Stop()`), so that
// retiring never blocks under the pool lock. The effect is therefore observable
// slightly AFTER Reap/Close returns, and asserting it synchronously races that
// goroutine — which is fine on an idle laptop and flakes on a loaded CI runner.
// Polling for it is the honest wait.
func waitStopped(t *testing.T, sp *fakeSpawner, n int, msg string) {
	t.Helper()
	for i := 0; i < 200; i++ { // ~1s
		if sp.stopped(n) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error(msg)
}

// A helper IS a user's credentials, so one per user is the model rather than a
// cache size: two users cannot share one, and one user gains nothing from two.
func TestOneHelperPerUser(t *testing.T) {
	p, sp, _ := newPool(t, Config{})

	for i := 0; i < 5; i++ {
		statDot(t, p, "alice")
	}
	if sp.count() != 1 {
		t.Errorf("spawned %d helpers for one user, want 1", sp.count())
	}

	statDot(t, p, "bob")
	if sp.count() != 2 {
		t.Errorf("spawned %d, want 2 after a second user", sp.count())
	}
	if p.Len() != 2 {
		t.Errorf("Len = %d, want 2", p.Len())
	}
}

// A helper keeps the groups it was started with for its whole life. Adding
// someone to a team would otherwise have no effect until the helper happened to
// be reaped — and removing them from one would leave the access working.
func TestGroupChangeReplacesTheHelper(t *testing.T) {
	// CredsTTL 1ns: re-resolve on every acquire, so the change is seen at once.
	p, sp, res := newPool(t, Config{CredsTTL: time.Nanosecond})

	statDot(t, p, "alice")
	if sp.count() != 1 {
		t.Fatalf("spawned %d, want 1", sp.count())
	}

	// alice joins team-b.
	res.set("alice", Creds{UID: 3001, GID: 3001, Groups: []uint32{3001, 10001, 10002}})
	statDot(t, p, "alice")

	if sp.count() != 2 {
		t.Fatalf("spawned %d, want 2 — the helper must be replaced when groups change", sp.count())
	}
	if got := sp.specs[1].Creds.Groups; len(got) != 3 {
		t.Errorf("replacement started with groups %v, want the new set", got)
	}
	waitStopped(t, sp, 0, "the stale helper must be stopped, not left running with the old groups")
	if p.Len() != 1 {
		t.Errorf("Len = %d, want 1", p.Len())
	}
}

// Only a real change counts, and "real" is decided by SameGroups. The name
// service is free to answer in any order and to repeat an entry; treating either
// as a change would restart helpers for no reason. NSSResolver therefore sorts
// and deduplicates before returning, and this pins what the comparison means.
func TestSameGroups(t *testing.T) {
	base := Creds{UID: 3001, GID: 3001, Groups: []uint32{3001, 10001}}

	for name, tt := range map[string]struct {
		other Creds
		want  bool
	}{
		"identical":       {Creds{UID: 3001, GID: 3001, Groups: []uint32{3001, 10001}}, true},
		"added a team":    {Creds{UID: 3001, GID: 3001, Groups: []uint32{3001, 10001, 10002}}, false},
		"removed a team":  {Creds{UID: 3001, GID: 3001, Groups: []uint32{3001}}, false},
		"primary changed": {Creds{UID: 3001, GID: 9999, Groups: []uint32{3001, 10001}}, false},
		// The uid is not part of the question: a helper for a different uid is a
		// different helper entirely, keyed by a different user name.
		"different uid": {Creds{UID: 4242, GID: 3001, Groups: []uint32{3001, 10001}}, true},
		// Order is the resolver's to normalise; if it ever stopped doing so, this
		// is where the resulting churn would be visible.
		"reordered": {Creds{UID: 3001, GID: 3001, Groups: []uint32{10001, 3001}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := base.SameGroups(tt.other); got != tt.want {
				t.Errorf("SameGroups(%v) = %v, want %v", tt.other.Groups, got, tt.want)
			}
		})
	}
}

// Between refreshes the cached resolution is reused, so a request does not cost
// an NSS round trip. That delay is the price of not making every request wait on
// the name service.
func TestCredentialsAreCachedForTheTTL(t *testing.T) {
	p, _, res := newPool(t, Config{CredsTTL: time.Hour})

	for i := 0; i < 10; i++ {
		statDot(t, p, "alice")
	}
	if res.calls != 1 {
		t.Errorf("resolver called %d times, want 1 within the TTL", res.calls)
	}
}

func TestReapsIdleHelpers(t *testing.T) {
	p, sp, _ := newPool(t, Config{IdleTimeout: time.Millisecond})

	statDot(t, p, "alice")
	if p.Len() != 1 {
		t.Fatalf("Len = %d, want 1", p.Len())
	}

	time.Sleep(5 * time.Millisecond)
	p.Reap()

	if p.Len() != 0 {
		t.Errorf("Len = %d, want 0 after the idle timeout", p.Len())
	}
	waitStopped(t, sp, 0, "a reaped helper must actually be stopped")
}

// A reap must never cut off an operation that has already been permitted, for a
// reason that has nothing to do with it.
func TestBusyHelperIsNotReaped(t *testing.T) {
	p, sp, _ := newPool(t, Config{IdleTimeout: time.Nanosecond})

	e, err := p.acquire(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	p.Reap()

	if p.Len() != 1 {
		t.Errorf("Len = %d, want 1 — a helper with a request in flight must survive", p.Len())
	}
	if sp.stopped(0) {
		t.Fatal("a busy helper was stopped")
	}

	// Once released it becomes reapable like any other.
	p.release(e)
	p.Reap()
	if p.Len() != 0 {
		t.Errorf("Len = %d, want 0 once idle", p.Len())
	}
}

func TestMaxHelpersEvictsLeastRecentlyUsed(t *testing.T) {
	p, sp, _ := newPool(t, Config{MaxHelpers: 2, IdleTimeout: time.Hour})

	statDot(t, p, "alice")
	statDot(t, p, "bob")
	statDot(t, p, "alice") // alice is now the most recent, bob the least
	statDot(t, p, "carol")

	if p.Len() != 2 {
		t.Errorf("Len = %d, want 2 (the cap)", p.Len())
	}
	// bob was evicted, so serving him again costs a new helper while alice's is
	// still the one from before.
	before := sp.count()
	statDot(t, p, "alice")
	if sp.count() != before {
		t.Error("alice's helper should have survived; she was used more recently than bob")
	}
	statDot(t, p, "bob")
	if sp.count() != before+1 {
		t.Error("bob should have been evicted and need a fresh helper")
	}
}

// When every helper is busy, failing beats queueing: the caller would be waiting
// on an unrelated user's work with no bound on how long.
func TestAllBusyFailsRatherThanQueueing(t *testing.T) {
	p, _, _ := newPool(t, Config{MaxHelpers: 1})

	e, err := p.acquire(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer p.release(e)

	if _, _, err := p.Do(context.Background(), "bob", &wire.Request{Op: wire.OpStat, Path: "."}); err == nil {
		t.Fatal("expected an error when the cap is reached and nothing can be evicted")
	}
}

func TestCloseStopsEveryHelper(t *testing.T) {
	p, sp, _ := newPool(t, Config{})
	statDot(t, p, "alice")
	statDot(t, p, "bob")

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.Len() != 0 {
		t.Errorf("Len = %d, want 0", p.Len())
	}
	// The stops are asynchronous (retire runs them in a goroutine), so poll for
	// each rather than assuming a fixed delay is enough on a loaded runner.
	for i := 0; i < sp.count(); i++ {
		waitStopped(t, sp, i, fmt.Sprintf("helper %d still running after Close", i))
	}
	if _, _, err := p.Do(context.Background(), "alice", &wire.Request{Op: wire.OpStat, Path: "."}); err == nil {
		t.Error("a closed pool must refuse new work")
	}
}

func TestUnknownUserFails(t *testing.T) {
	p, sp, _ := newPool(t, Config{})
	if _, _, err := p.Do(context.Background(), "nobody", &wire.Request{Op: wire.OpStat, Path: "."}); err == nil {
		t.Fatal("expected an error for an unresolvable user")
	}
	if sp.count() != 0 {
		t.Error("nothing should be spawned for a user that does not resolve")
	}
}

func TestConcurrentUsersDoNotRaceOverHelpers(t *testing.T) {
	p, sp, _ := newPool(t, Config{})

	var wg sync.WaitGroup
	for _, user := range []string{"alice", "bob", "carol"} {
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(u string) {
				defer wg.Done()
				resp, _, err := p.Do(context.Background(), u, &wire.Request{Op: wire.OpStat, Path: "."})
				if err != nil || !resp.OK() {
					t.Errorf("Do as %s: %v", u, err)
				}
			}(user)
		}
	}
	wg.Wait()

	if sp.count() != 3 {
		t.Errorf("spawned %d helpers for 3 users, want 3", sp.count())
	}
}
