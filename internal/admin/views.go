package admin

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Where a reader actually reads.
//
// A reader group is granted by usersync as a read-only, id-mapped mount of the
// team's folder placed inside the READER group's own folder — not by anything
// written on the files (usersync roster.Group.Readers). So on disk there are two
// paths to the same data:
//
//	teams/<team>              the team's own folder. A reader is not in the team
//	                          group, so this one refuses them.
//	teams/<reader>/<team>     the view. Read-only, and it looks like theirs.
//
// The web's answer to a reader asking for teams/<team> must be the second path.
// Everything else about the request stays as it was: the path a share link
// holds, the path the activity log records and the path the interface shows are
// all the team's own, because that is what the person means. Only the syscall
// goes somewhere else.
//
// Nothing here decides an access. Routing a reader to the view lets the kernel
// say yes to a read it was always meant to allow; routing them wrongly, or not
// at all, ends in EACCES or EROFS. There is no way for a mistake in this file to
// grant somebody something — which is why it can be driven by a cached roster.

// teamsDir is the directory every team folder lives in, as the helper sees it.
const teamsDir = "teams"

// declTTL is how long a roster read is reused for routing.
//
// Routing happens per filesystem operation and reading the roster is a
// subprocess, so it cannot be done per call. The staleness this buys is the same
// kind the helper pool already has (a helper keeps the groups it started with
// until it is replaced), and it is bounded on the safe side: a membership that
// has not arrived yet costs a reader a few seconds of EACCES, never access they
// should not have.
const declTTL = 5 * time.Second

type declCache struct {
	mu   sync.Mutex
	decl *Declaration
	at   time.Time
}

// cachedDeclaration returns the roster, re-reading it at most every declTTL.
func (a *Admin) cachedDeclaration(ctx context.Context) (*Declaration, error) {
	a.decls.mu.Lock()
	defer a.decls.mu.Unlock()
	if a.decls.decl != nil && time.Since(a.decls.at) < declTTL {
		return a.decls.decl, nil
	}
	d, err := a.Declaration(ctx)
	if err != nil {
		// Keep serving the last good answer rather than silently un-routing every
		// reader the moment `usersync roster` hiccups. A stale route still cannot
		// grant anything.
		if a.decls.decl != nil {
			return a.decls.decl, nil
		}
		return nil, err
	}
	a.decls.decl, a.decls.at = d, time.Now()
	return d, nil
}

// ReaderRoutes maps each team the user reads through a view to the path of that
// view, relative to the data root — the path the helper opens instead.
//
// The view's path comes from the roster (usersync's `views`), not from a rule
// of ours: where views live is usersync's to decide, and a rule kept here as
// well can drift from it. A usersync that predates `views` (schema 0) laid them
// out at teams/<reader>/<team>, so that is built only then. A view usersync
// places outside the data root has no route: the reader is refused, which is the
// direction to fail in.
//
// A team the user is a MEMBER of is never in the map: they reach their own
// folder directly, and sending them to the read-only view would take their
// writes away. Membership is read from the roster — the same source
// TeamAccess uses, and the one that has already resolved `all` cohorts into
// concrete members.
func (a *Admin) ReaderRoutes(ctx context.Context, user string) (map[string]string, error) {
	d, err := a.cachedDeclaration(ctx)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{}
	for _, g := range d.Groups {
		if slices.Contains(g.Members, user) {
			mine[g.Name] = true
		}
	}
	type key struct{ reader, team string }
	declared := map[key]string{}
	for _, g := range d.Groups {
		for _, v := range g.Views {
			declared[key{g.Name, v.Team}] = v.Path
		}
	}
	out := map[string]string{}
	for _, g := range d.Groups {
		if mine[g.Name] {
			continue
		}
		// Sorted, so a team read through two of the user's groups always resolves
		// to the same one. The views are equivalent — both read-only, both of the
		// same folder — so which is picked does not matter, only that it is stable
		// (an unstable choice would make identical requests differ).
		readers := slices.Clone(g.Readers)
		slices.Sort(readers)
		for _, r := range readers {
			if !mine[r] {
				continue
			}
			if d.Schema == 0 {
				out[g.Name] = path.Join(teamsDir, r, g.Name)
			} else if rel, ok := a.underRoot(declared[key{r, g.Name}]); ok {
				out[g.Name] = rel
			}
			break
		}
	}
	return out, nil
}

// underRoot turns an absolute path usersync gave into one relative to the data
// root the helper serves, or reports that it is not inside it.
func (a *Admin) underRoot(p string) (string, bool) {
	if p == "" || !filepath.IsAbs(p) {
		return "", false
	}
	rel, err := filepath.Rel(a.cfg.Root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// ReroutePath returns the path the helper should actually open for user, given
// the path the person asked for. Anything that is not a reader's request at a
// team folder comes back unchanged.
func (a *Admin) ReroutePath(ctx context.Context, user, p string) string {
	if a == nil || p == "" {
		return p
	}
	// teams/<team>[/...]. The root itself, a home, and a path already inside a
	// reader's own folder are all left alone — the last of these is what makes
	// this idempotent, since the view's own path starts teams/<reader>/ and the
	// user is a member of <reader>.
	rest, ok := strings.CutPrefix(path.Clean(p), teamsDir+"/")
	if !ok || rest == "" {
		return p
	}
	team, tail, _ := strings.Cut(rest, "/")
	routes, err := a.ReaderRoutes(ctx, user)
	if err != nil {
		// Unrouted means the reader hits the team's own folder and is refused.
		// That is the direction to fail in, and it is visible as a permission
		// error rather than as data somebody should not have seen.
		return p
	}
	out, ok := routes[team]
	if !ok {
		return p
	}
	if tail != "" {
		out = path.Join(out, tail)
	}
	return out
}
