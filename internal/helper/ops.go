package helper

import (
	"os"
	"path"
	"sort"
	"strings"

	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// resolve opens a path relative to the root, letting the kernel enforce that it
// stays there. No string inspection happens first: `..`, an absolute path and a
// symlink pointing outside are all rejected by RESOLVE_BENEATH, and any check
// written here would only be a second, weaker copy of that.
func (h *Helper) resolve(rel string, flags uint64, mode uint64) (int, error) {
	if rel == "" {
		rel = "."
	}
	return unix.Openat2(h.rootFD, rel, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC,
		Mode:    mode,
		Resolve: resolveFlags,
	})
}

// resolveParent splits a path into its parent directory and final component,
// opens the parent under the same guarantee, and returns both.
//
// The *at() calls that create and remove names — mkdirat, unlinkat, renameat2,
// linkat — do NOT accept openat2's resolve flags, so handing them a path with
// `..` in it would walk straight out of the root. Resolving the parent through
// openat2 and passing a single, validated component leaves them nothing to
// resolve: the directory is already known to be inside, and the name cannot
// refer anywhere else.
func (h *Helper) resolveParent(rel string) (dirfd int, base string, err error) {
	dir, base := path.Split(rel)
	// path.Split guarantees base holds no separator; the rest are the components
	// that would still mean "somewhere other than here".
	if base == "" || base == "." || base == ".." || strings.ContainsRune(base, 0) {
		return -1, "", unix.EINVAL
	}
	if dir == "" {
		dir = "."
	}
	fd, err := h.resolve(dir, unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, "", err
	}
	return fd, base, nil
}

func statOf(st *unix.Stat_t) *wire.Stat {
	return &wire.Stat{
		Mode:      st.Mode,
		UID:       st.Uid,
		GID:       st.Gid,
		Size:      st.Size,
		MtimeSec:  st.Mtim.Sec,
		MtimeNsec: uint32(st.Mtim.Nsec),
		Ino:       st.Ino,
		Nlink:     uint64(st.Nlink),
	}
}

// dispatch performs one operation. The returned descriptor is -1 unless the
// operation produced one; the caller sends it as SCM_RIGHTS and closes it.
func (h *Helper) dispatch(req *wire.Request) (*wire.Response, int) {
	switch req.Op {
	case wire.OpOpen:
		fd, err := h.resolve(req.Path, uint64(req.Flags), uint64(req.Mode))
		if err != nil {
			return fail(req.ID, err)
		}
		return &wire.Response{ID: req.ID, Has: wire.HasFD}, fd

	case wire.OpMkdir:
		dirfd, base, err := h.resolveParent(req.Path)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(dirfd)
		// mkdir(2) masks the mode down to 0777 — setgid, setuid and the sticky bit
		// are silently dropped, and normally arrive only by inheritance from the
		// parent. A team directory IS its setgid bit: without it, files created
		// inside take the creator's own group instead of the team's, so everyone
		// else gets read-only on them and nothing in the failure points at the
		// cause. It therefore has to be set explicitly, after the directory exists.
		if err := unix.Mkdirat(dirfd, base, req.Mode&0o777); err != nil {
			return fail(req.ID, err)
		}
		if req.Mode&^uint32(0o777) != 0 {
			if err := unix.Fchmodat(dirfd, base, req.Mode&0o7777, 0); err != nil {
				// Leaving a half-made directory would be worse than failing: a team
				// folder missing its setgid bit looks fine and behaves wrongly.
				_ = unix.Unlinkat(dirfd, base, unix.AT_REMOVEDIR)
				return fail(req.ID, err)
			}
		}
		return ok(req.ID)

	case wire.OpUnlink, wire.OpRmdir:
		dirfd, base, err := h.resolveParent(req.Path)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(dirfd)
		flags := 0
		if req.Op == wire.OpRmdir {
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(dirfd, base, flags); err != nil {
			return fail(req.ID, err)
		}
		return ok(req.ID)

	case wire.OpRename, wire.OpLink:
		oldfd, oldbase, err := h.resolveParent(req.Path)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(oldfd)
		newfd, newbase, err := h.resolveParent(req.Path2)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(newfd)

		if req.Op == wire.OpLink {
			// The write protocol pins the old inode in the trash with a link and
			// then replaces the name with a single rename, so a reader never sees a
			// moment where the target is missing.
			if err := unix.Linkat(oldfd, oldbase, newfd, newbase, 0); err != nil {
				return fail(req.ID, err)
			}
			return ok(req.ID)
		}
		if err := unix.Renameat2(oldfd, oldbase, newfd, newbase, uint(req.Flags)); err != nil {
			return fail(req.ID, err)
		}
		return ok(req.ID)

	case wire.OpStat:
		flags := uint64(unix.O_PATH)
		if req.Flags&wire.FlagNoFollow != 0 {
			flags |= unix.O_NOFOLLOW
		}
		fd, err := h.resolve(req.Path, flags, 0)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return fail(req.ID, err)
		}
		return &wire.Response{ID: req.ID, Stat: statOf(&st)}, -1

	case wire.OpChmod:
		if req.Flags&wire.FlagNoFollow != 0 {
			// fchmodat's AT_SYMLINK_NOFOLLOW is unimplemented on Linux, and a
			// symlink has no mode of its own to change. Saying so beats pretending.
			return fail(req.ID, unix.EOPNOTSUPP)
		}
		dirfd, base, err := h.resolveParent(req.Path)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(dirfd)
		if err := unix.Fchmodat(dirfd, base, req.Mode, 0); err != nil {
			return fail(req.ID, err)
		}
		return ok(req.ID)

	case wire.OpSetXattr:
		fd, err := h.resolve(req.Path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(fd)
		if err := unix.Fsetxattr(fd, req.Name, req.Value, 0); err != nil {
			return fail(req.ID, err)
		}
		return ok(req.ID)

	case wire.OpGetXattr:
		fd, err := h.resolve(req.Path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fail(req.ID, err)
		}
		defer unix.Close(fd)
		// Two-call sizing: ask for the length, then read exactly that. A file
		// with no such attribute answers ENODATA, which the server reads as
		// "no ACL" rather than an error — the whole point of the call.
		size, err := unix.Fgetxattr(fd, req.Name, nil)
		if err != nil {
			return fail(req.ID, err)
		}
		buf := make([]byte, size)
		n, err := unix.Fgetxattr(fd, req.Name, buf)
		if err != nil {
			return fail(req.ID, err)
		}
		return &wire.Response{ID: req.ID, Has: wire.HasValue, Value: buf[:n]}, -1

	case wire.OpReadDir:
		return h.readDir(req)
	}
	return fail(req.ID, unix.EINVAL)
}

// entryCost is what one directory entry adds to the frame: a u16-prefixed name,
// the type byte, the stat-present byte, and the fixed stat when included.
const (
	entryFixed = 2 + 1 + 1
	statSize   = 4 + 4 + 4 + 8 + 8 + 4 + 8 + 8
	// headroom covers the response header plus the entry count.
	headroom = 8 + 4 + 1 + 4
)

// readDir lists a directory, optionally stat'ing each entry.
//
// The stat happens here rather than server-side because fstatat is a path-based
// call: issued by the root server it would be checked against root, not the
// user. Doing it in the same pass also turns N+1 round trips into one, which
// matters because a listing screen needs size and mtime for every row.
//
// Entries are sorted by name so a truncated listing can be resumed from the last
// name returned — readdir order is not stable enough to resume from otherwise.
func (h *Helper) readDir(req *wire.Request) (*wire.Response, int) {
	fd, err := h.resolve(req.Path, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fail(req.ID, err)
	}
	f := os.NewFile(uintptr(fd), req.Path)
	defer f.Close()

	ents, err := f.ReadDir(-1)
	if err != nil {
		return fail(req.ID, err)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })

	withStat := req.Flags&wire.FlagWithStat != 0
	resp := &wire.Response{ID: req.ID, Has: wire.HasEntries}
	budget := wire.MaxFrame - headroom

	for _, e := range ents {
		name := e.Name()
		if req.Name != "" && name <= req.Name {
			continue // resuming: everything up to the cursor was already sent
		}
		cost := entryFixed + len(name)
		if withStat {
			cost += statSize
		}
		if cost > budget {
			resp.Has |= wire.HasMore
			break
		}
		budget -= cost

		de := wire.DirEntry{Name: name, Type: direntType(e)}
		if withStat {
			var st unix.Stat_t
			// AT_SYMLINK_NOFOLLOW: a listing describes the entries themselves, and
			// following a link here would report the target's size and mtime — or
			// fail outright on a dangling one, losing the whole row.
			if err := unix.Fstatat(int(f.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
				de.Stat = statOf(&st)
			}
		}
		resp.Entries = append(resp.Entries, de)
	}
	return resp, -1
}

// direntType maps Go's file mode back to the DT_* value getdents reported.
func direntType(e os.DirEntry) uint8 {
	switch t := e.Type(); {
	case t.IsRegular():
		return unix.DT_REG
	case t.IsDir():
		return unix.DT_DIR
	case t&os.ModeSymlink != 0:
		return unix.DT_LNK
	case t&os.ModeDevice != 0 && t&os.ModeCharDevice != 0:
		return unix.DT_CHR
	case t&os.ModeDevice != 0:
		return unix.DT_BLK
	case t&os.ModeNamedPipe != 0:
		return unix.DT_FIFO
	case t&os.ModeSocket != 0:
		return unix.DT_SOCK
	default:
		return unix.DT_UNKNOWN
	}
}
