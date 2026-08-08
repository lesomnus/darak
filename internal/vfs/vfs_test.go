package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/helper"
	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// oneUser adapts a single helper to the Doer interface. The write protocol does
// not care which user it runs as — that is the pool's concern — so the tests
// drive one real helper and check the order of operations it receives.
type oneUser struct{ c *helperpool.Client }

func (o oneUser) Do(ctx context.Context, _ string, req *wire.Request) (*wire.Response, *os.File, error) {
	return o.c.Do(ctx, req)
}

func newFS(t *testing.T) (*FS, string) {
	t.Helper()
	root := t.TempDir()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	helperEnd := os.NewFile(uintptr(fds[0]), "helper")
	clientEnd := os.NewFile(uintptr(fds[1]), "client")

	h, err := helper.New(root, helperEnd)
	helperEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = h.Serve() }()

	conn, err := net.FileConn(clientEnd)
	clientEnd.Close()
	if err != nil {
		t.Fatal(err)
	}
	c := helperpool.NewClient(conn.(*net.UnixConn))
	t.Cleanup(func() { c.Close(); h.Close() })

	// A fixed clock so trash names are predictable.
	at := time.Date(2026, 8, 8, 13, 22, 5, 0, time.UTC)
	fs := &FS{Pool: oneUser{c}, Now: func() time.Time { return at }}

	// The layout the domain rules assume.
	for _, d := range []string{"homes", "homes/alice", "teams", "teams/design"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return fs, root
}

func read(t *testing.T, root, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, p))
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func trashOf(t *testing.T, root, domain string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, domain, TrashDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteCreatesWithExactMode(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	if err := fs.Write(ctx, "alice", "homes/alice/notes.txt", strings.NewReader("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "homes/alice/notes.txt"); got != "v1" {
		t.Errorf("content = %q", got)
	}
	fi, err := os.Stat(filepath.Join(root, "homes/alice/notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	// Nothing was overwritten, so there is nothing to keep.
	for _, n := range trashOf(t, root, "homes/alice") {
		t.Errorf("trash should be empty for a first write, found %q", n)
	}
	// The temp file must not survive.
	assertNoTempFiles(t, root, "homes/alice")
}

// The point of the link-then-rename order: the old version is preserved AND the
// target never stops existing. Renaming the old file out of the way first would
// leave a window in which a reader gets ENOENT rather than either version.
func TestOverwriteKeepsTheOldVersion(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	if err := fs.Write(ctx, "alice", "teams/design/report.txt", strings.NewReader("draft"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(ctx, "alice", "teams/design/report.txt", strings.NewReader("final"), 0o660); err != nil {
		t.Fatal(err)
	}

	if got := read(t, root, "teams/design/report.txt"); got != "final" {
		t.Errorf("target = %q, want final", got)
	}
	names := trashOf(t, root, "teams/design")
	if len(names) != 1 {
		t.Fatalf("trash = %v, want exactly the overwritten version", names)
	}
	if got := read(t, root, filepath.Join("teams/design", TrashDir, names[0])); got != "draft" {
		t.Errorf("trashed copy = %q, want the version that was replaced", got)
	}
	assertNoTempFiles(t, root, "teams/design")
}

// A team file's previous version has to land where the team can reach it. Put in
// the author's home it would be invisible to everyone else, so the colleague who
// needs to restore it could not.
func TestTrashGoesToThePermissionDomain(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	if err := fs.Write(ctx, "alice", "teams/design/a.txt", strings.NewReader("1"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(ctx, "alice", "teams/design/a.txt", strings.NewReader("2"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(ctx, "alice", "homes/alice/b.txt", strings.NewReader("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(ctx, "alice", "homes/alice/b.txt", strings.NewReader("2"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := trashOf(t, root, "teams/design"); len(got) != 1 || !strings.HasSuffix(got[0], "_a.txt") {
		t.Errorf("team trash = %v, want the team's own file", got)
	}
	if got := trashOf(t, root, "homes/alice"); len(got) != 1 || !strings.HasSuffix(got[0], "_b.txt") {
		t.Errorf("home trash = %v, want the home's own file", got)
	}
}

// The trash is visible over SMB, and Windows cannot open a file whose name
// contains a colon — the entry would be there and unreachable from the client
// that most often needs it.
func TestTrashNamesAreUsableFromWindows(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := fs.Write(ctx, "alice", "homes/alice/x.txt", strings.NewReader("v"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names := trashOf(t, root, "homes/alice")
	if len(names) != 1 {
		t.Fatalf("trash = %v", names)
	}
	for _, bad := range []string{":", "<", ">", "\"", "|", "?", "*", "\\"} {
		if strings.Contains(names[0], bad) {
			t.Errorf("trash name %q contains %q, which Windows cannot open", names[0], bad)
		}
	}
	if !strings.HasPrefix(names[0], "2026-08-08T13-22-05_") {
		t.Errorf("trash name = %q, want a readable timestamp prefix", names[0])
	}
}

// Two overwrites within the same second must not collide into one entry, or the
// second one silently destroys the record of the first.
func TestTwoOverwritesInOneSecondBothSurvive(t *testing.T) {
	fs, root := newFS(t) // fixed clock: every call has the same timestamp
	ctx := context.Background()

	for _, v := range []string{"v1", "v2", "v3"} {
		if err := fs.Write(ctx, "alice", "homes/alice/x.txt", strings.NewReader(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names := trashOf(t, root, "homes/alice")
	if len(names) != 2 {
		t.Fatalf("trash = %v, want both replaced versions", names)
	}
	kept := map[string]bool{}
	for _, n := range names {
		kept[read(t, root, filepath.Join("homes/alice", TrashDir, n))] = true
	}
	if !kept["v1"] || !kept["v2"] {
		t.Errorf("trash holds %v, want v1 and v2", kept)
	}
}

// A failure part-way through must leave the target exactly as it was. The write
// is only visible at the rename, so there is no state in which a reader sees a
// truncated upload.
func TestFailedWriteLeavesTheTargetAlone(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	if err := fs.Write(ctx, "alice", "homes/alice/x.txt", strings.NewReader("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("upload aborted")
	err := fs.Write(ctx, "alice", "homes/alice/x.txt", io.MultiReader(
		strings.NewReader("partial"), errReader{boom},
	), 0o600)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader's error", err)
	}

	if got := read(t, root, "homes/alice/x.txt"); got != "original" {
		t.Errorf("target = %q, want it untouched", got)
	}
	// The old version was never linked away, so nothing should be in the trash.
	if names := trashOf(t, root, "homes/alice"); len(names) != 0 {
		t.Errorf("trash = %v, want empty: nothing was replaced", names)
	}
	assertNoTempFiles(t, root, "homes/alice")
}

func TestRemoveMovesToTrashAndEmptyingActuallyDeletes(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	if err := fs.Write(ctx, "alice", "homes/alice/x.txt", strings.NewReader("bye"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(ctx, "alice", "homes/alice/x.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "homes/alice/x.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the file should have left its original name")
	}
	names := trashOf(t, root, "homes/alice")
	if len(names) != 1 {
		t.Fatalf("trash = %v, want the deleted file", names)
	}
	// A delete has to be as recoverable as an overwrite: the argument for
	// accepting last-write-wins is that losing work is always undoable, and that
	// does not hold if delete is exempt.
	if got := read(t, root, filepath.Join("homes/alice", TrashDir, names[0])); got != "bye" {
		t.Errorf("trashed copy = %q", got)
	}

	// Emptying the trash must really remove things, or it fills up forever.
	if err := fs.Remove(ctx, "alice", filepath.Join("homes/alice", TrashDir, names[0])); err != nil {
		t.Fatal(err)
	}
	if got := trashOf(t, root, "homes/alice"); len(got) != 0 {
		t.Errorf("trash = %v, want empty", got)
	}
}

func TestDomainRoot(t *testing.T) {
	for in, want := range map[string]string{
		"homes/alice/notes.txt":      "homes/alice",
		"homes/alice":                "homes/alice",
		"teams/design/sub/deep/x":    "teams/design",
		"teams/design/.trash/old.md": "teams/design",
	} {
		got, err := DomainRoot(in)
		if err != nil {
			t.Errorf("DomainRoot(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("DomainRoot(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything outside the two domains has no correct trash location, so writing
	// there must be refused rather than guessed at.
	for _, in := range []string{"", ".", "x.txt", "homes", "teams", "other/thing/x", "homes/../etc/passwd"} {
		if got, err := DomainRoot(in); err == nil {
			t.Errorf("DomainRoot(%q) = %q, want an error", in, got)
		}
	}
}

func TestWriteOutsideADomainIsRefused(t *testing.T) {
	fs, root := newFS(t)
	if err := fs.Write(context.Background(), "alice", "stray.txt", strings.NewReader("x"), 0o600); err == nil {
		t.Fatal("a path with no permission domain must be refused")
	}
	if _, err := os.Stat(filepath.Join(root, "stray.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("nothing should have been created")
	}
}

func TestReadDirCollectsEveryPage(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	const n = 3000
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file-%05d-padded-out-so-the-listing-needs-several-frames", i)
		if err := os.WriteFile(filepath.Join(root, "homes/alice", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ents, err := fs.ReadDir(ctx, "alice", "homes/alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != n {
		t.Fatalf("got %d entries, want %d — the resume loop dropped some", len(ents), n)
	}
	seen := map[string]bool{}
	for _, e := range ents {
		if seen[e.Name] {
			t.Fatalf("%q appeared twice", e.Name)
		}
		seen[e.Name] = true
		if e.Stat == nil {
			t.Fatalf("%q has no stat", e.Name)
		}
	}
}

func TestErrnoIsPassedThrough(t *testing.T) {
	fs, _ := newFS(t)
	_, err := fs.Open(context.Background(), "alice", "homes/alice/missing")
	var e *Errno
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *Errno", err)
	}
	// The distinction between absent and denied is the kernel's, and a caller
	// needs it to answer 404 rather than 403.
	if e.Err != unix.ENOENT {
		t.Errorf("errno = %v, want ENOENT", e.Err)
	}
	if !errors.Is(err, unix.ENOENT) {
		t.Error("errors.Is should reach the errno")
	}
}

func assertNoTempFiles(t *testing.T, root, dir string) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Errorf("temp file left behind: %s/%s", dir, e.Name())
		}
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// The two access paths must produce identical modes, or "same data, same
// permission rules" is false exactly where users would notice: a file uploaded
// through the web that a teammate cannot edit, while the one they dragged into
// the SMB share is fine.
func TestModesMirrorTheSambaMasks(t *testing.T) {
	for p, want := range map[string]uint32{
		"homes/alice/x.txt":    0o600,
		"teams/design/x.txt":   0o660,
		"teams/design/a/b.txt": 0o660,
	} {
		got, err := CreateMode(p)
		if err != nil {
			t.Fatalf("CreateMode(%q): %v", p, err)
		}
		if got != want {
			t.Errorf("CreateMode(%q) = %04o, want %04o", p, got, want)
		}
	}
	for p, want := range map[string]uint32{
		"homes/alice/sub":  0o700,
		"teams/design/sub": 0o2770,
	} {
		got, err := DirMode(p)
		if err != nil {
			t.Fatalf("DirMode(%q): %v", p, err)
		}
		if got != want {
			t.Errorf("DirMode(%q) = %04o, want %04o", p, got, want)
		}
	}
	// A team directory without setgid would put files created inside it in the
	// creator's own group, and every teammate would be read-only on them.
	if m, _ := DirMode("teams/design/sub"); m&0o2000 == 0 {
		t.Error("a team directory must be setgid")
	}
	if _, err := CreateMode("stray.txt"); err == nil {
		t.Error("a path outside any domain has no defined mode and must be refused")
	}
}

func TestIsTempName(t *testing.T) {
	if !IsTempName(tmpPrefix + "abc") {
		t.Error("a partial upload must be recognised so listings can hide it")
	}
	if IsTempName("report.txt") || IsTempName(".trash") {
		t.Error("ordinary names must not be hidden")
	}
}

// The trash is a directory this package creates on its own, so it is the one
// place the domain's mode has to be applied by hand — and the only place it was
// forgotten. Created 0700 in a team folder it belongs to whoever overwrote
// something first, and every other member's next write fails at the link step
// with EACCES: the file untouched, the error opaque, and the cause a directory
// nobody thinks to look at. That is the exact failure the setgid layout exists
// to prevent, reintroduced one level down.
func TestTrashDirectoryModeFollowsTheDomain(t *testing.T) {
	fs, root := newFS(t)
	ctx := context.Background()

	for _, tt := range []struct {
		path string
		want os.FileMode
	}{
		{"teams/design/x.txt", os.ModeSetgid | 0o770},
		{"homes/alice/x.txt", 0o700},
	} {
		// Two writes: the second is what creates the trash.
		for i := 0; i < 2; i++ {
			if err := fs.Write(ctx, "alice", tt.path, strings.NewReader("v"), 0o660); err != nil {
				t.Fatal(err)
			}
		}
		domain, err := DomainRoot(tt.path)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(root, domain, TrashDir))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode() &^ os.ModeDir; got != tt.want {
			t.Errorf("%s trash mode = %v, want %v", domain, got, tt.want)
		}
	}
}
