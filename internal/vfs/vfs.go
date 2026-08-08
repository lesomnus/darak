// Package vfs is the file layer the server actually calls: every operation is
// performed by the requesting user's helper, and the write path is the protocol
// from nas-design.md §6 rather than an open-and-write.
//
// Nothing here consults permissions. The kernel decides, in the helper, with the
// user's own credentials, and an errno comes back untranslated.
package vfs

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// TrashDir is the directory name used at the root of every permission domain.
const TrashDir = ".trash"

// tmpPrefix marks a partial upload. A crash leaves one behind, and a cron sweep
// removes them; the name has to be recognisable for that to be possible.
const tmpPrefix = ".upload-"

// Errno is a kernel verdict, passed through so callers can map it to a status
// code without this layer deciding what it means.
type Errno struct {
	Op   string
	Path string
	Err  unix.Errno
}

func (e *Errno) Error() string { return fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err) }
func (e *Errno) Unwrap() error { return e.Err }

func errnoOf(op, p string, code uint32) error {
	if code == 0 {
		return nil
	}
	return &Errno{Op: op, Path: p, Err: unix.Errno(code)}
}

// Doer performs one request as a named user. It is satisfied by
// *helperpool.Pool.
//
// vfs needs nothing from the pool but this, and depending on the interface keeps
// the write protocol testable against a single helper, with no process
// lifecycle involved in checking that a rename happened in the right order.
type Doer interface {
	Do(ctx context.Context, user string, req *wire.Request) (*wire.Response, *os.File, error)
}

// FS performs operations as a named user.
type FS struct {
	Pool Doer

	// Now is overridable so trash names are deterministic in tests.
	Now func() time.Time
}

func (f *FS) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *FS) do(ctx context.Context, user string, req *wire.Request) (*wire.Response, *os.File, error) {
	return f.Pool.Do(ctx, user, req)
}

// Open opens a path for reading.
func (f *FS) Open(ctx context.Context, user, p string) (*os.File, error) {
	resp, file, err := f.do(ctx, user, &wire.Request{Op: wire.OpOpen, Path: p, Flags: unix.O_RDONLY})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, errnoOf("open", p, resp.Errno)
	}
	return file, nil
}

// Stat returns a path's metadata without following a final symlink.
func (f *FS) Stat(ctx context.Context, user, p string) (*wire.Stat, error) {
	resp, _, err := f.do(ctx, user, &wire.Request{Op: wire.OpStat, Path: p, Flags: wire.FlagNoFollow})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, errnoOf("stat", p, resp.Errno)
	}
	return resp.Stat, nil
}

// ReadDir lists a directory, following the protocol's resume rule until the
// whole listing has been collected.
func (f *FS) ReadDir(ctx context.Context, user, p string, withStat bool) ([]wire.DirEntry, error) {
	var flags uint32
	if withStat {
		flags |= wire.FlagWithStat
	}
	var out []wire.DirEntry
	cursor := ""
	for {
		resp, _, err := f.do(ctx, user, &wire.Request{
			Op: wire.OpReadDir, Path: p, Flags: flags, Name: cursor,
		})
		if err != nil {
			return nil, err
		}
		if !resp.OK() {
			return nil, errnoOf("readdir", p, resp.Errno)
		}
		out = append(out, resp.Entries...)
		if resp.Has&wire.HasMore == 0 {
			return out, nil
		}
		if len(resp.Entries) == 0 {
			// The helper said there is more but sent nothing, so the cursor cannot
			// advance. Looping would spin forever.
			return nil, fmt.Errorf("vfs: readdir %q made no progress", p)
		}
		cursor = out[len(out)-1].Name
	}
}

// Mkdir creates a directory with an exact mode.
func (f *FS) Mkdir(ctx context.Context, user, p string, mode uint32) error {
	resp, _, err := f.do(ctx, user, &wire.Request{Op: wire.OpMkdir, Path: p, Mode: mode})
	if err != nil {
		return err
	}
	return errnoOf("mkdir", p, resp.Errno)
}

// DomainRoot returns the permission domain a path belongs to: a user's home or a
// team's folder.
//
// It is what decides where a trashed file goes, and getting it wrong is not
// cosmetic. Sending a team file to the author's home would put it somewhere no
// other member can reach, so the person who needs to restore it cannot; sending
// a home file to a team folder would expose it.
func DomainRoot(p string) (string, error) {
	parts := strings.Split(path.Clean(p), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("vfs: %q is not inside a permission domain", p)
	}
	switch parts[0] {
	case "homes", "teams":
		if parts[1] == "" || parts[1] == "." || parts[1] == ".." {
			return "", fmt.Errorf("vfs: %q is not inside a permission domain", p)
		}
		return parts[0] + "/" + parts[1], nil
	default:
		return "", fmt.Errorf("vfs: %q is not inside a permission domain", p)
	}
}

// trashName builds the name an overwritten or deleted file is kept under.
//
// The timestamp uses '-' rather than ':' because the trash is visible over SMB
// and Windows cannot open a file whose name contains a colon — the entry would
// be there and unreachable from the client that most often needs it.
func (f *FS) trashName(p string) string {
	return f.now().UTC().Format("2006-01-02T15-04-05") + "_" + path.Base(p)
}

// Write stores src at p, following the write protocol:
//
//	temp file -> fsync -> link the old inode into the trash -> one rename -> fsync the parent
//
// The link is what makes it a single replacement. Renaming the old file out of
// the way first would leave a window where the name does not exist at all, and a
// reader in that window gets ENOENT rather than either version — so "readers see
// the old file or the new one, never an in-between" would simply not be true.
func (f *FS) Write(ctx context.Context, user, p string, src io.Reader, mode uint32) (err error) {
	dir := path.Dir(p)
	if dir == "." || dir == "/" {
		return fmt.Errorf("vfs: %q has no parent directory", p)
	}
	domain, err := DomainRoot(p)
	if err != nil {
		return err
	}

	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	tmp := path.Join(dir, tmpPrefix+suffix)

	// 1. Create the temp file beside the target, so the rename that follows stays
	// within one filesystem and is therefore atomic.
	resp, file, err := f.do(ctx, user, &wire.Request{
		Op: wire.OpOpen, Path: tmp,
		Flags: unix.O_CREAT | unix.O_EXCL | unix.O_WRONLY, Mode: mode,
	})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return errnoOf("create", tmp, resp.Errno)
	}
	defer func() {
		// Any failure from here on leaves the target untouched; the temp file is
		// the only thing to clean up.
		if err != nil {
			file.Close()
			_ = f.unlink(ctx, user, tmp)
		}
	}()

	// 2. Content, then fsync: a rename that publishes a name whose data is not on
	// disk turns a crash into a zero-length file where the old one used to be.
	if _, err = io.Copy(file, src); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}

	// 3. Pin the current version in the trash. The target keeps existing.
	if err = f.linkToTrash(ctx, user, p, domain); err != nil {
		return err
	}

	// 4. One atomic rename.
	resp, _, err = f.do(ctx, user, &wire.Request{Op: wire.OpRename, Path: tmp, Path2: p})
	if err != nil {
		return err
	}
	if err = errnoOf("rename", p, resp.Errno); err != nil {
		return err
	}

	// 5. Durability of the name itself. Without this a crash can lose the
	// directory entry even though the data was synced.
	return f.syncDir(ctx, user, dir)
}

// linkToTrash hard-links the current version of p into its domain's trash.
//
// The link is attempted unconditionally rather than after a stat: checking first
// leaves a window in which the file appears, and it would then be overwritten
// with no copy kept. An absent target is simply nothing to preserve.
func (f *FS) linkToTrash(ctx context.Context, user, p, domain string) error {
	trash := path.Join(domain, TrashDir)
	if err := f.Mkdir(ctx, user, trash, 0o700); err != nil {
		var e *Errno
		if !errors.As(err, &e) || e.Err != unix.EEXIST {
			return err
		}
	}

	// A second overwrite within the same second would collide; the suffix makes
	// the name unique without losing the timestamp's readability.
	name := f.trashName(p)
	for attempt := 0; ; attempt++ {
		resp, _, err := f.do(ctx, user, &wire.Request{
			Op: wire.OpLink, Path: p, Path2: path.Join(trash, name),
		})
		if err != nil {
			return err
		}
		switch unix.Errno(resp.Errno) {
		case 0:
			return nil
		case unix.ENOENT:
			return nil // no current version: nothing to keep
		case unix.EEXIST:
			if attempt >= 8 {
				return errnoOf("link", name, resp.Errno)
			}
			suffix, err := randomSuffix()
			if err != nil {
				return err
			}
			name = f.trashName(p) + "-" + suffix
		default:
			return errnoOf("link", p, resp.Errno)
		}
	}
}

// Remove moves a path into its domain's trash instead of unlinking it.
//
// A delete is as recoverable as an overwrite here, which is the point: the
// design accepts last-write-wins precisely because losing work is always
// undoable, and that argument does not hold if delete is exempt from it.
func (f *FS) Remove(ctx context.Context, user, p string) error {
	domain, err := DomainRoot(p)
	if err != nil {
		return err
	}
	if path.Base(path.Dir(p)) == TrashDir {
		// Already in the trash: emptying it has to actually remove things.
		return f.unlink(ctx, user, p)
	}
	trash := path.Join(domain, TrashDir)
	if err := f.Mkdir(ctx, user, trash, 0o700); err != nil {
		var e *Errno
		if !errors.As(err, &e) || e.Err != unix.EEXIST {
			return err
		}
	}
	resp, _, err := f.do(ctx, user, &wire.Request{
		Op: wire.OpRename, Path: p, Path2: path.Join(trash, f.trashName(p)),
	})
	if err != nil {
		return err
	}
	return errnoOf("remove", p, resp.Errno)
}

func (f *FS) unlink(ctx context.Context, user, p string) error {
	resp, _, err := f.do(ctx, user, &wire.Request{Op: wire.OpUnlink, Path: p})
	if err != nil {
		return err
	}
	return errnoOf("unlink", p, resp.Errno)
}

// syncDir flushes a directory so a newly published name survives a crash.
func (f *FS) syncDir(ctx context.Context, user, dir string) error {
	resp, file, err := f.do(ctx, user, &wire.Request{
		Op: wire.OpOpen, Path: dir, Flags: unix.O_RDONLY | unix.O_DIRECTORY,
	})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return errnoOf("open", dir, resp.Errno)
	}
	defer file.Close()
	return file.Sync()
}

// randomSuffix names a temp or colliding trash entry. It is unguessable so two
// concurrent writers to the same target cannot pick the same temp name — with
// O_EXCL that would be an error rather than corruption, but a retry loop is
// worse than not colliding.
func randomSuffix() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}
