package vfs

import (
	"context"
	"errors"

	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// WalkLimits bounds a recursive walk.
//
// Every one of these is required, not advisory. A tree on a file server has no
// natural size, and a search with no ceiling is a request that holds a helper,
// a goroutine and a connection for as long as the data happens to be large --
// which is exactly when someone is most likely to be searching it.
type WalkLimits struct {
	// Depth is how many levels below the starting directory to descend. 0 lists
	// only the starting directory itself.
	Depth int
	// Visit caps how many entries are EXAMINED, not how many are returned. It has
	// to be the former: the query that finds nothing is the one that walks
	// furthest, and a cap on results would not bound it at all.
	Visit int
}

// WalkResult says how the walk ended, which is not the same as what it found.
type WalkResult struct {
	// Visited is how many entries were examined.
	Visited int
	// Truncated means a limit stopped the walk before the tree ran out. The
	// caller must pass this on: a silently shortened list is indistinguishable
	// from "it is not here", and those are opposite answers.
	Truncated bool
}

// Walk visits every entry below p, breadth first, as user.
//
// It is built out of ReadDir, which means it needs nothing new from the helper
// protocol. That is deliberate. A recursive walk could be one WALK operation
// instead of one round trip per directory, and it would have to live in the
// process that holds the user's credentials, carry its own resume cursor across
// 64 KiB frames, and enforce these limits -- policy, inside the component whose
// whole value is that it has none. The round trips are a local socketpair and
// cost microseconds; the directories themselves are the expense either way.
//
// Breadth first, so the shallow entries -- the ones nearer where the person
// started, and so the ones more likely to be meant -- are found first. With a
// streaming caller that is also the order results appear on screen.
//
// visit is called for every entry with its path relative to p. Returning false
// stops the walk, and that is not counted as truncation: the caller got what it
// asked for.
//
// Entries are NOT stat'd. A search needs a name and whether it is a directory,
// and DT_DIR comes back from getdents for free; asking for stat would add an
// fstatat per entry for two columns nobody is looking at yet.
func (f *FS) Walk(
	ctx context.Context,
	user, p string,
	limits WalkLimits,
	visit func(rel string, e wire.DirEntry) bool,
) (WalkResult, error) {
	var res WalkResult

	// Each level's directories, relative to p. "" is p itself.
	level := []string{""}
	for depth := 0; depth <= limits.Depth && len(level) > 0; depth++ {
		var next []string
		// A directory at the bottom level that is not being descended into. The
		// count of `next` cannot stand in for this: at the last level nothing is
		// ever queued, so it is always empty and the walk would report itself
		// complete precisely when it was not.
		deeper := false
		for _, dir := range level {
			if err := ctx.Err(); err != nil {
				// A client that navigated away or typed another character is not an
				// error to report -- it is the reason this stopped, and the caller
				// already knows.
				res.Truncated = true
				return res, nil
			}

			full := p
			if dir != "" {
				full = p + "/" + dir
			}
			ents, err := f.ReadDir(ctx, user, full, false)
			if err != nil {
				// A directory that cannot be read is not a failure of the search.
				// Permission is the normal case: `teams/` lists every team and you are
				// in two of them. Skipping is the same answer walking there by hand
				// would give.
				if isErrno(err, unix.EACCES, unix.EPERM, unix.ENOENT, unix.ENOTDIR) {
					continue
				}
				return res, err
			}

			for _, e := range ents {
				// A crashed upload is not a file anyone is looking for, and the trash
				// is a different question from "where is my file" -- a deleted copy
				// surfacing next to live ones, looking identical, is worse than not
				// finding it.
				if IsTempName(e.Name) || e.Name == TrashDir {
					continue
				}
				if res.Visited >= limits.Visit {
					res.Truncated = true
					return res, nil
				}
				res.Visited++

				rel := e.Name
				if dir != "" {
					rel = dir + "/" + e.Name
				}
				if !visit(rel, e) {
					return res, nil
				}
				if e.Type == unix.DT_DIR {
					if depth < limits.Depth {
						next = append(next, rel)
					} else {
						deeper = true
					}
				}
			}
		}
		// There was more tree and the budget said no. Say so rather than
		// returning a complete-looking answer.
		if deeper {
			res.Truncated = true
		}
		level = next
	}
	return res, nil
}

func isErrno(err error, codes ...unix.Errno) bool {
	var e *Errno
	if !errors.As(err, &e) {
		return false
	}
	for _, c := range codes {
		if e.Err == c {
			return true
		}
	}
	return false
}
