package helperpool

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lesomnus/darak/internal/helper"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// newPair runs a helper over a socketpair in this process and returns a client
// for the other end.
//
// This exercises the real protocol, the real openat2 resolution and real fd
// passing; what it cannot exercise is the privilege drop, since the helper is
// running as whoever runs `go test`. The uid separation is covered by the
// container test — see internal/integration.
//
// Note that helper.New sets umask(0) process-wide, which is what makes the mode
// assertions below meaningful. It is harmless here and correct in production,
// where the helper is its own process.
func newPair(t *testing.T, root string) *Client {
	t.Helper()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	helperEnd := os.NewFile(uintptr(fds[0]), "helper")
	clientEnd := os.NewFile(uintptr(fds[1]), "client")

	h, err := helper.New(root, helperEnd)
	helperEnd.Close() // net.FileConn dup'd it
	if err != nil {
		t.Fatalf("helper.New: %v", err)
	}
	served := make(chan struct{})
	go func() { defer close(served); _ = h.Serve() }()

	conn, err := net.FileConn(clientEnd)
	clientEnd.Close()
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	c := NewClient(conn.(*net.UnixConn))
	t.Cleanup(func() {
		c.Close()
		h.Close()
		<-served
	})
	return c
}

func do(t *testing.T, c *Client, req *wire.Request) (*wire.Response, *os.File) {
	t.Helper()
	resp, f, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do(%s %q): %v", req.Op, req.Path, err)
	}
	return resp, f
}

func mustOK(t *testing.T, resp *wire.Response, what string) {
	t.Helper()
	if !resp.OK() {
		t.Fatalf("%s: errno %d (%v)", what, resp.Errno, unix.Errno(resp.Errno))
	}
}

func TestOpenReadWrite(t *testing.T) {
	root := t.TempDir()
	c := newPair(t, root)

	// Create with an explicit mode. umask is not consulted, so the mode is exact.
	resp, f := do(t, c, &wire.Request{
		Op:    wire.OpOpen,
		Path:  "hello.txt",
		Flags: unix.O_CREAT | unix.O_WRONLY,
		Mode:  0o640,
	})
	mustOK(t, resp, "create")
	if f == nil {
		t.Fatal("create returned no descriptor")
	}
	if _, err := f.WriteString("well hello"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	fi, err := os.Stat(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %04o, want 0640 — the requested mode must not be filtered by a umask", fi.Mode().Perm())
	}

	resp, f = do(t, c, &wire.Request{Op: wire.OpOpen, Path: "hello.txt", Flags: unix.O_RDONLY})
	mustOK(t, resp, "open")
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "well hello" {
		t.Errorf("read %q", got)
	}
}

// Nothing in the helper inspects a path string. These all have to be refused by
// the kernel, via RESOLVE_BENEATH — which is the point of not writing the checks
// by hand, since each of these is a different way to be wrong about what a path
// means.
func TestEscapesAreRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing straight out of the root.
	if err := os.Symlink("/etc", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	// ...and one that climbs out with "..".
	if err := os.Symlink("../../..", filepath.Join(root, "climb")); err != nil {
		t.Fatal(err)
	}
	c := newPair(t, root)

	for _, path := range []string{
		"../etc/passwd",
		"sub/../../etc/passwd",
		"/etc/passwd",
		"escape/passwd",
		"climb/etc/passwd",
		"..",
	} {
		t.Run(path, func(t *testing.T) {
			resp, f := do(t, c, &wire.Request{Op: wire.OpOpen, Path: path, Flags: unix.O_RDONLY})
			if f != nil {
				f.Close()
			}
			if resp.OK() {
				t.Fatalf("%q was opened; it must not resolve outside the root", path)
			}
		})
	}
}

// The namespace calls do not honour openat2's resolve flags, so the escape has
// to be stopped by resolving the parent and passing a single component. A
// destination that climbs out must fail on the same terms as a read would.
func TestNamespaceOpsCannotEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newPair(t, root)

	for _, tt := range []struct {
		name string
		req  *wire.Request
	}{
		{"rename out", &wire.Request{Op: wire.OpRename, Path: "src", Path2: "../escaped"}},
		{"rename from out", &wire.Request{Op: wire.OpRename, Path: "../../etc/hosts", Path2: "stolen"}},
		{"link out", &wire.Request{Op: wire.OpLink, Path: "src", Path2: "../escaped"}},
		{"mkdir out", &wire.Request{Op: wire.OpMkdir, Path: "../escaped", Mode: 0o755}},
		{"unlink out", &wire.Request{Op: wire.OpUnlink, Path: "../something"}},
		{"mkdir dotdot", &wire.Request{Op: wire.OpMkdir, Path: "..", Mode: 0o755}},
		{"mkdir dot", &wire.Request{Op: wire.OpMkdir, Path: ".", Mode: 0o755}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, f := do(t, c, tt.req)
			if f != nil {
				f.Close()
			}
			if resp.OK() {
				t.Fatalf("%s succeeded; it must not reach outside the root", tt.name)
			}
		})
	}
}

// mkdir(2) masks its mode down to 0777, so setgid never survives the syscall
// and normally arrives only by inheritance from the parent. A team directory is
// defined by that bit — without it, files created inside take the creator's own
// group and every teammate silently gets read-only — so the helper has to put it
// back explicitly. This asserts the whole requested mode, including the bits the
// syscall drops.
func TestMkdirModeIncludesSetgid(t *testing.T) {
	root := t.TempDir()
	c := newPair(t, root)

	resp, _ := do(t, c, &wire.Request{Op: wire.OpMkdir, Path: "team", Mode: 0o2770})
	mustOK(t, resp, "mkdir")

	fi, err := os.Stat(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o770 {
		t.Errorf("perm = %04o, want 0770", perm)
	}
	if fi.Mode()&os.ModeSetgid == 0 {
		t.Error("setgid bit was lost — mkdir(2) drops it, so it has to be set afterwards")
	}

	// A plain directory must not pick up bits nobody asked for.
	resp, _ = do(t, c, &wire.Request{Op: wire.OpMkdir, Path: "plain", Mode: 0o700})
	mustOK(t, resp, "mkdir plain")
	fi, err = os.Stat(filepath.Join(root, "plain"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != os.ModeDir|0o700 {
		t.Errorf("mode = %v, want drwx------", fi.Mode())
	}
}

// The write protocol from the design doc: temp file, then link the old inode
// into the trash, then ONE rename. The point of the link is that the target
// never stops existing — the two-rename version has a window where a reader
// gets ENOENT.
func TestWriteProtocolLeavesNoWindow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("v1"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".trash"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := newPair(t, root)

	resp, f := do(t, c, &wire.Request{
		Op: wire.OpOpen, Path: ".upload-tmp",
		Flags: unix.O_CREAT | unix.O_WRONLY | unix.O_EXCL, Mode: 0o660,
	})
	mustOK(t, resp, "create temp")
	if _, err := f.WriteString("v2"); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	resp, _ = do(t, c, &wire.Request{Op: wire.OpLink, Path: "report.txt", Path2: ".trash/report.txt"})
	mustOK(t, resp, "link to trash")

	// The target is still there, and still the old content, between the two steps.
	if b, err := os.ReadFile(filepath.Join(root, "report.txt")); err != nil || string(b) != "v1" {
		t.Fatalf("target must remain readable and unchanged between link and rename: %q %v", b, err)
	}

	resp, _ = do(t, c, &wire.Request{Op: wire.OpRename, Path: ".upload-tmp", Path2: "report.txt"})
	mustOK(t, resp, "rename into place")

	if b, _ := os.ReadFile(filepath.Join(root, "report.txt")); string(b) != "v2" {
		t.Errorf("target = %q, want v2", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".trash/report.txt")); string(b) != "v1" {
		t.Errorf("trash = %q, want the overwritten v1", b)
	}
}

func TestRenameNoReplace(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newPair(t, root)

	resp, _ := do(t, c, &wire.Request{
		Op: wire.OpRename, Path: "a", Path2: "b", Flags: unix.RENAME_NOREPLACE,
	})
	if resp.OK() {
		t.Fatal("RENAME_NOREPLACE must refuse to clobber an existing name")
	}
	if resp.Errno != uint32(unix.EEXIST) {
		t.Errorf("errno = %v, want EEXIST", unix.Errno(resp.Errno))
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b")); string(b) != "b" {
		t.Errorf("b was clobbered: %q", b)
	}
}

func TestStatAndReadDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	c := newPair(t, root)

	resp, _ := do(t, c, &wire.Request{Op: wire.OpStat, Path: "file"})
	mustOK(t, resp, "stat")
	if resp.Stat == nil || resp.Stat.Size != 5 {
		t.Fatalf("stat = %#v, want size 5", resp.Stat)
	}

	resp, _ = do(t, c, &wire.Request{Op: wire.OpReadDir, Path: ".", Flags: wire.FlagWithStat})
	mustOK(t, resp, "readdir")
	if resp.Has&wire.HasEntries == 0 {
		t.Fatal("a listing must set HasEntries even to say it is empty")
	}

	byName := map[string]wire.DirEntry{}
	for _, e := range resp.Entries {
		byName[e.Name] = e
	}
	if len(byName) != 3 {
		t.Fatalf("entries = %v, want file/dir/dangling", byName)
	}
	if e := byName["file"]; e.Type != unix.DT_REG || e.Stat == nil || e.Stat.Size != 5 {
		t.Errorf("file entry = %#v", e)
	}
	if e := byName["dir"]; e.Type != unix.DT_DIR {
		t.Errorf("dir entry type = %d, want DT_DIR", e.Type)
	}
	// A dangling symlink must still appear, with its own stat rather than the
	// missing target's — following it would drop the row entirely.
	if e := byName["dangling"]; e.Type != unix.DT_LNK || e.Stat == nil {
		t.Errorf("dangling symlink entry = %#v; it must be listed with its own stat", e)
	}

	// Entries come back sorted, which is what makes the resume cursor work.
	for i := 1; i < len(resp.Entries); i++ {
		if resp.Entries[i-1].Name >= resp.Entries[i].Name {
			t.Fatalf("entries are not sorted: %v", resp.Entries)
		}
	}
}

// A truncated listing must resume from the last name returned and cover every
// entry exactly once.
func TestReadDirResume(t *testing.T) {
	root := t.TempDir()
	const n = 4000
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("entry-%06d-with-a-fairly-long-name-to-fill-the-frame", i)
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newPair(t, root)

	seen := map[string]bool{}
	cursor := ""
	for rounds := 0; ; rounds++ {
		if rounds > 100 {
			t.Fatal("listing did not terminate")
		}
		resp, _ := do(t, c, &wire.Request{
			Op: wire.OpReadDir, Path: ".", Flags: wire.FlagWithStat, Name: cursor,
		})
		mustOK(t, resp, "readdir")
		if len(resp.Entries) == 0 && resp.Has&wire.HasMore != 0 {
			t.Fatal("a page with no entries must not claim there is more")
		}
		for _, e := range resp.Entries {
			if seen[e.Name] {
				t.Fatalf("%q was returned twice", e.Name)
			}
			seen[e.Name] = true
		}
		if resp.Has&wire.HasMore == 0 {
			break
		}
		if len(resp.Entries) == 0 {
			t.Fatal("no progress")
		}
		cursor = resp.Entries[len(resp.Entries)-1].Name
	}
	if len(seen) != n {
		t.Errorf("saw %d entries, want %d", len(seen), n)
	}
}

// Replies carry an id and may come back in any order, so a slow request must not
// hold up the ones behind it. With one helper per user there is nothing else to
// route around a stall.
func TestConcurrentRequestsAreMultiplexed(t *testing.T) {
	root := t.TempDir()
	const n = 200
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%03d", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newPair(t, root)

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("f%03d", i)
			resp, f, err := c.Do(context.Background(), &wire.Request{
				Op: wire.OpOpen, Path: name, Flags: unix.O_RDONLY,
			})
			if err != nil {
				errs <- err
				return
			}
			if !resp.OK() {
				errs <- fmt.Errorf("%s: errno %v", name, unix.Errno(resp.Errno))
				return
			}
			defer f.Close()
			got, err := io.ReadAll(f)
			if err != nil {
				errs <- err
				return
			}
			// The wrong descriptor coming back would show up here as content from
			// another request — the failure a mismatched id would produce.
			if string(got) != name {
				errs <- fmt.Errorf("%s: got %q", name, got)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestUnknownPathReportsENOENT(t *testing.T) {
	c := newPair(t, t.TempDir())
	resp, f := do(t, c, &wire.Request{Op: wire.OpOpen, Path: "nope", Flags: unix.O_RDONLY})
	if f != nil {
		f.Close()
	}
	// The kernel's verdict is passed through untranslated; the server needs to be
	// able to tell absent from denied.
	if resp.Errno != uint32(unix.ENOENT) {
		t.Errorf("errno = %v, want ENOENT", unix.Errno(resp.Errno))
	}
}
