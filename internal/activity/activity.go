// Package activity records who created, changed or deleted which file.
//
// There is no OS-level answer available here, which is worth stating because it
// is the first thing anyone reaches for. The kernel audit subsystem is not
// namespaced — one daemon per host, owned by the host — and a container cannot
// register with it even with --privileged (auditctl returns EPERM). fanotify
// needs CAP_SYS_ADMIN, cannot place a filesystem-wide mark on the container's
// own filesystem, and reports a PID that has to be resolved to a uid after the
// fact, racily, against a process that may already be gone.
//
// What is available is better than either, because both ways into this server
// already know the AUTHENTICATED identity rather than inferring one:
//
//   - SMB goes through smbd, where the full_audit VFS module reports the
//     logged-in username for every operation — including from a kernel cifs
//     mount, since a mounted share is still SMB underneath.
//   - The web path runs every operation as the session's user through the
//     helper (nas-design.md ADR-3), so darak knows who acted with certainty and
//     records its own events.
//
// Retention is a rolling window. Keeping everything is a backup's job, and the
// store is plain per-day JSONL in the state volume precisely so that copying
// the directory somewhere is the whole of it.
package activity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source is where an event was observed.
type Source string

const (
	// SourceSMB came from smbd's full_audit module.
	SourceSMB Source = "smb"
	// SourceWeb came from darak's own handlers.
	SourceWeb Source = "web"
)

// Action is what happened. Deliberately a small set: the question this answers
// is "who touched my file", and a faithful transcript of every SMB operation
// answers it worse than four verbs do.
type Action string

const (
	Created Action = "create"
	Wrote   Action = "write"
	Deleted Action = "delete"
	Renamed Action = "rename"
	Mkdir   Action = "mkdir"
)

// Event is one recorded change.
type Event struct {
	At   time.Time `json:"at"`
	User string    `json:"user"`
	// Action is what was done.
	Action Action `json:"action"`
	// Path is relative to the served root, so an event reads the same as the
	// interface's own addresses and does not leak the container's layout.
	Path string `json:"path"`
	// To is the destination of a rename; empty otherwise.
	To string `json:"to,omitempty"`
	// Source distinguishes an SMB client from the web interface. Worth keeping:
	// "it was not me, I only use the web page" is a real thing people say, and
	// the two paths are separately explainable.
	Source Source `json:"source"`
	// From is the client address for an SMB event. The web path has one too but
	// it is the proxy's as often as not, so it is only set where it means
	// something.
	From string `json:"from,omitempty"`
}

// Query filters a listing.
type Query struct {
	// User, Path and Action are substring/prefix filters; empty means any.
	User   string
	Path   string
	Action Action
	// Since bounds the window; zero means as far back as the store goes.
	Since time.Time
	// Limit caps the result. Zero means DefaultLimit.
	Limit int
}

// DefaultLimit bounds an unfiltered listing. A file server generates a lot of
// events and a page that tries to render all of them helps nobody.
const DefaultLimit = 500

func (q Query) matches(e Event) bool {
	if q.User != "" && e.User != q.User {
		return false
	}
	if q.Action != "" && e.Action != q.Action {
		return false
	}
	if q.Path != "" && !strings.Contains(e.Path, q.Path) && !strings.Contains(e.To, q.Path) {
		return false
	}
	if !q.Since.IsZero() && e.At.Before(q.Since) {
		return false
	}
	return true
}

// sortNewestFirst orders events for display. Stable so two events in the same
// millisecond keep the order they were recorded in, which is the order they
// happened.
func sortNewestFirst(es []Event) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].At.After(es[j].At) })
}

// MarshalLine renders an event as one JSONL record.
func (e Event) MarshalLine() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseLine reads one JSONL record.
func ParseLine(b []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, fmt.Errorf("activity: parse event: %w", err)
	}
	return e, nil
}
