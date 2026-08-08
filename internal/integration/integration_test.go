//go:build integration

// Package integration exercises the helper against real, separate unix users.
//
// Everything else can be tested in one process, but not the thing the design
// actually rests on: that the kernel refuses one user access to another's files
// because of who the helper is running as. That needs real uids, real groups and
// real modes, which needs root — so this runs in a throwaway container. See
// scripts/verify-integration.sh.
package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

const (
	// dataRoot mirrors the layout in nas-design.md §5. It is deliberately not
	// t.TempDir(): that is 0700 and owned by root, so a helper running as an
	// ordinary user could not even traverse into it.
	dataRoot  = "/srv/data"
	helperBin = "/usr/local/bin/darak-helper"

	aliceUID, aliceGID = 3001, 3001
	bobUID, bobGID     = 3002, 3002
	teamGID            = 10001
)

func TestMain(m *testing.M) {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "integration: needs root and real accounts; run scripts/verify-integration.sh")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// setupWorld builds the accounts and the directory layout the design specifies:
// private homes at 0700, a team folder at 2770 with setgid.
func setupWorld(t *testing.T) {
	t.Helper()

	if _, err := os.Stat(helperBin); err != nil {
		t.Fatalf("helper binary missing at %s: %v", helperBin, err)
	}

	run(t, "groupadd", "-f", "-g", "10001", "team-a")
	for _, u := range []struct {
		name     string
		uid, gid int
	}{{"alice", aliceUID, aliceGID}, {"bob", bobUID, bobGID}} {
		if _, err := exec.Command("id", u.name).Output(); err != nil {
			run(t, "groupadd", "-g", fmt.Sprint(u.gid), u.name)
			run(t, "useradd", "-M", "-u", fmt.Sprint(u.uid), "-g", fmt.Sprint(u.gid),
				"-d", filepath.Join(dataRoot, "homes", u.name), "-s", "/usr/sbin/nologin", u.name)
		}
		run(t, "usermod", "-aG", "team-a", u.name)
	}

	// The root itself has to be traversable by everyone; what is private is what
	// sits underneath it.
	for _, d := range []string{dataRoot, filepath.Join(dataRoot, "homes"), filepath.Join(dataRoot, "teams")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []struct {
		name string
		uid  int
	}{{"alice", aliceUID}, {"bob", bobUID}} {
		home := filepath.Join(dataRoot, "homes", u.name)
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(home, u.uid, u.uid); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(home, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	team := filepath.Join(dataRoot, "teams", "team-a")
	if err := os.MkdirAll(team, 0o2770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(team, 0, teamGID); err != nil {
		t.Fatal(err)
	}
	// Chmod after Chown: changing the owner clears setgid.
	if err := os.Chmod(team, os.ModeSetgid|0o770); err != nil {
		t.Fatal(err)
	}
}

func spawn(t *testing.T, uid, gid uint32, groups []uint32) *helperpool.Helper {
	t.Helper()
	h, err := helperpool.Spawn(helperpool.Spec{
		Bin: helperBin, Root: dataRoot,
		UID: uid, GID: gid, Groups: groups,
	})
	if err != nil {
		t.Fatalf("spawn helper for uid %d: %v", uid, err)
	}
	t.Cleanup(func() { _ = h.Stop() })
	return h
}

func do(t *testing.T, h *helperpool.Helper, req *wire.Request) (*wire.Response, *os.File) {
	t.Helper()
	resp, f, err := h.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do(%s %q): %v", req.Op, req.Path, err)
	}
	return resp, f
}

// The whole design rests on this: a helper can reach exactly what its user can
// reach, because the kernel decides and the helper is that user.
func TestUserCannotReadAnotherUsersHome(t *testing.T) {
	setupWorld(t)

	secret := filepath.Join(dataRoot, "homes", "bob", "diary.txt")
	if err := os.WriteFile(secret, []byte("bob's private notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(secret, bobUID, bobGID); err != nil {
		t.Fatal(err)
	}

	alice := spawn(t, aliceUID, aliceGID, []uint32{aliceGID, teamGID})

	resp, f := do(t, alice, &wire.Request{
		Op: wire.OpOpen, Path: "homes/bob/diary.txt", Flags: unix.O_RDONLY,
	})
	if f != nil {
		f.Close()
		t.Fatal("alice was handed a descriptor for bob's file")
	}
	if resp.OK() {
		t.Fatal("alice opened bob's file")
	}
	if resp.Errno != uint32(unix.EACCES) {
		t.Errorf("errno = %v, want EACCES", unix.Errno(resp.Errno))
	}

	// Listing bob's home must fail for the same reason, so the names inside are
	// not disclosed either.
	resp, _ = do(t, alice, &wire.Request{Op: wire.OpReadDir, Path: "homes/bob"})
	if resp.OK() {
		t.Error("alice listed bob's home")
	}

	// And her own home is reachable, so the refusal above is about permission
	// rather than the helper being broken.
	resp, f = do(t, alice, &wire.Request{
		Op: wire.OpOpen, Path: "homes/alice/mine.txt",
		Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0o600,
	})
	if !resp.OK() {
		t.Fatalf("alice cannot write in her own home: %v", unix.Errno(resp.Errno))
	}
	f.Close()

	fi, err := os.Stat(filepath.Join(dataRoot, "homes", "alice", "mine.txt"))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != aliceUID {
		t.Errorf("file created through alice's helper is owned by uid %d, want %d", st.Uid, aliceUID)
	}
}

// A file created in the team folder must be usable by the other member. This is
// the property the whole permission layout exists to produce, and the one that
// fails silently when a mode is wrong.
func TestTeammateCanEditWhatTheOtherCreated(t *testing.T) {
	setupWorld(t)

	alice := spawn(t, aliceUID, aliceGID, []uint32{aliceGID, teamGID})
	bob := spawn(t, bobUID, bobGID, []uint32{bobGID, teamGID})

	resp, f := do(t, alice, &wire.Request{
		Op: wire.OpOpen, Path: "teams/team-a/report.txt",
		Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0o660,
	})
	if !resp.OK() {
		t.Fatalf("alice could not create in the team folder: %v", unix.Errno(resp.Errno))
	}
	if _, err := f.WriteString("draft by alice"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	path := filepath.Join(dataRoot, "teams", "team-a", "report.txt")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	// setgid on the parent is what puts the file in the team's group rather than
	// alice's own. Without it every teammate would be on the "other" bits.
	if st.Gid != teamGID {
		t.Errorf("group = %d, want %d (setgid inheritance)", st.Gid, teamGID)
	}
	if fi.Mode().Perm()&0o060 != 0o060 {
		t.Errorf("mode = %04o, want group rw", fi.Mode().Perm())
	}

	// The actual test: bob writes to it.
	resp, f = do(t, bob, &wire.Request{
		Op: wire.OpOpen, Path: "teams/team-a/report.txt", Flags: unix.O_WRONLY | unix.O_APPEND,
	})
	if !resp.OK() {
		t.Fatalf("bob cannot write to alice's team file: %v — this is the failure that looks like nothing is wrong", unix.Errno(resp.Errno))
	}
	if _, err := f.WriteString(" + edit by bob"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "draft by alice + edit by bob" {
		t.Errorf("content = %q", got)
	}
}

// A subdirectory made through the helper has to carry setgid too, or the same
// failure reappears one level down.
func TestTeamSubdirectoryKeepsSetgid(t *testing.T) {
	setupWorld(t)
	alice := spawn(t, aliceUID, aliceGID, []uint32{aliceGID, teamGID})
	bob := spawn(t, bobUID, bobGID, []uint32{bobGID, teamGID})

	resp, _ := do(t, alice, &wire.Request{Op: wire.OpMkdir, Path: "teams/team-a/sub", Mode: 0o2770})
	if !resp.OK() {
		t.Fatalf("mkdir: %v", unix.Errno(resp.Errno))
	}
	fi, err := os.Stat(filepath.Join(dataRoot, "teams", "team-a", "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Fatal("subdirectory lost setgid; files created inside would take the creator's group")
	}

	resp, f := do(t, bob, &wire.Request{
		Op: wire.OpOpen, Path: "teams/team-a/sub/notes.txt",
		Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0o660,
	})
	if !resp.OK() {
		t.Fatalf("bob cannot create inside the subdirectory alice made: %v", unix.Errno(resp.Errno))
	}
	f.Close()

	fi, err = os.Stat(filepath.Join(dataRoot, "teams", "team-a", "sub", "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st := fi.Sys().(*syscall.Stat_t); st.Gid != teamGID {
		t.Errorf("group = %d, want %d", st.Gid, teamGID)
	}
}

// The kernel refuses the escape, with the helper's own credentials, on a real
// filesystem — not just in a temp directory owned by the test process.
func TestEscapesAreRefusedForARealUser(t *testing.T) {
	setupWorld(t)
	alice := spawn(t, aliceUID, aliceGID, []uint32{aliceGID, teamGID})

	for _, path := range []string{
		"../etc/shadow",
		"/etc/shadow",
		"homes/../../etc/shadow",
	} {
		t.Run(path, func(t *testing.T) {
			resp, f := do(t, alice, &wire.Request{Op: wire.OpOpen, Path: path, Flags: unix.O_RDONLY})
			if f != nil {
				b, _ := io.ReadAll(f)
				f.Close()
				t.Fatalf("%q resolved outside the root and returned %d bytes", path, len(b))
			}
			if resp.OK() {
				t.Fatalf("%q was opened", path)
			}
		})
	}
}

// A helper must never run as root: every permission check it made would pass.
func TestSpawnRefusesRoot(t *testing.T) {
	if _, err := helperpool.Spawn(helperpool.Spec{Bin: helperBin, Root: dataRoot, UID: 0, GID: 0}); err == nil {
		t.Fatal("spawning a helper as root must be refused")
	}
}
