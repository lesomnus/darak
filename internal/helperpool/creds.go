package helperpool

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Creds is what a helper has to be started with.
type Creds struct {
	UID uint32
	GID uint32
	// Groups is the complete supplementary set, sorted, so two resolutions can
	// be compared directly.
	Groups []uint32
}

// SameGroups reports whether two resolutions grant the same memberships.
//
// This is the question the pool asks: a helper keeps the groups it was started
// with for its whole life, so adding someone to a team has no effect until the
// helper is replaced. Comparing is how that gets noticed.
func (c Creds) SameGroups(other Creds) bool {
	return c.GID == other.GID && slices.Equal(c.Groups, other.Groups)
}

// Resolver looks up a user's credentials.
type Resolver interface {
	Resolve(ctx context.Context, user string) (Creds, error)
}

// NSSResolver resolves through NSS by running getent and id.
//
// It shells out rather than reading /etc/passwd, and rather than using Go's
// os/user, because both of those see only local files under CGO_ENABLED=0 — and
// the whole point of the AD roadmap is that the accounts eventually do not live
// there. getent and id go through the name service, so winbind answers.
type NSSResolver struct{}

func (NSSResolver) Resolve(ctx context.Context, user string) (Creds, error) {
	if user == "" || strings.ContainsAny(user, ":\n\x00") {
		return Creds{}, fmt.Errorf("helperpool: invalid user name %q", user)
	}

	out, err := exec.CommandContext(ctx, "getent", "passwd", user).Output()
	if err != nil {
		return Creds{}, fmt.Errorf("helperpool: no such user %q", user)
	}
	// name:x:uid:gid:gecos:home:shell
	f := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(f) < 4 {
		return Creds{}, fmt.Errorf("helperpool: unexpected getent output for %q", user)
	}
	uid, err := strconv.ParseUint(f[2], 10, 32)
	if err != nil {
		return Creds{}, fmt.Errorf("helperpool: parse uid for %q: %w", user, err)
	}
	gid, err := strconv.ParseUint(f[3], 10, 32)
	if err != nil {
		return Creds{}, fmt.Errorf("helperpool: parse gid for %q: %w", user, err)
	}

	// `id -G` is getgrouplist: the full set including the primary group, resolved
	// through NSS. Reading /etc/group instead would miss every domain membership.
	out, err = exec.CommandContext(ctx, "id", "-G", user).Output()
	if err != nil {
		return Creds{}, fmt.Errorf("helperpool: group list for %q: %w", user, err)
	}
	var groups []uint32
	for _, s := range strings.Fields(string(out)) {
		g, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return Creds{}, fmt.Errorf("helperpool: parse group %q for %q: %w", s, user, err)
		}
		groups = append(groups, uint32(g))
	}
	// Sorted and deduplicated so a reordering by the name service does not read
	// as a membership change and pointlessly restart the helper.
	slices.Sort(groups)
	groups = slices.Compact(groups)

	return Creds{UID: uint32(uid), GID: uint32(gid), Groups: groups}, nil
}
