package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/lesomnus/darak/internal/admin"
)

// Batch team-membership changes, with something to watch.
//
// A single add or remove already goes through the control plane, which edits the
// version-controlled roster and lets usersync reconcile the system to it. That
// makes one change one commit and one convergence — fine for one, but an operator
// adjusting a team tends to make several at once, and doing them one at a time is
// several commits, several syncs, and several waits. So the page stages the
// changes and confirms them together: /api/teams/apply lands the whole set as one
// revision, and /api/teams/status streams how far the reconcile it started has
// got, the same way an enrollment streams its stage. "Applied" is this server's
// answer, read from NSS — the moment the group table actually reflects the change.

// reconcileTracker remembers, per apply id, the changes to watch for, so the
// status stream can poll NSS until the system reflects them. Lost on a restart,
// which just means the page stops following an apply that has already committed —
// the convergence runs regardless.
type reconcileTracker struct {
	mu sync.Mutex
	m  map[string]reconcileEntry
}

type reconcileEntry struct {
	changes []admin.MembershipChange
	expires time.Time
}

func newReconcileTracker() *reconcileTracker {
	return &reconcileTracker{m: map[string]reconcileEntry{}}
}

func (t *reconcileTracker) put(id string, changes []admin.MembershipChange, exp time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[id] = reconcileEntry{changes: changes, expires: exp}
}

func (t *reconcileTracker) get(id string) (reconcileEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[id]
	return v, ok
}

func (t *reconcileTracker) sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, v := range t.m {
		if now.After(v.expires) {
			delete(t.m, id)
		}
	}
}

func newApplyID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// handleTeamsApply lands a batch of membership changes as one, and hands back an
// id the page follows for its progress.
//
//	POST /api/teams/apply  {"changes": [{"team": "...", "user": "...", "member": true|false}]}
//	→ 200 {"id": "..."}
//
// The batch is authorized and validated whole in the admin layer; a caller who
// may not manage even one team named changes nothing. The id is only minted after
// the commit lands, so following it means following a change that is really on its
// way.
func (s *Server) handleTeamsApply(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Admin == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Changes []admin.MembershipChange `json:"changes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "expected {\"changes\": [{\"team\": \"...\", \"user\": \"...\", \"member\": true|false}]}")
		return
	}
	if len(body.Changes) == 0 {
		writeError(w, http.StatusBadRequest, "no changes")
		return
	}
	if len(body.Changes) > 256 {
		writeError(w, http.StatusBadRequest, "too many changes at once")
		return
	}

	err := s.cfg.Admin.BatchSetTeamMembership(r.Context(), userOf(r), body.Changes)
	switch {
	case errors.Is(err, admin.ErrNotOwner):
		// 404, like the single-change route: a signed-in stranger learns nothing
		// about which teams exist or who owns them.
		http.NotFound(w, r)
		return
	case errors.Is(err, admin.ErrUnknownUser):
		writeError(w, http.StatusNotFound, "no such managed account")
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	id := newApplyID()
	s.reconcile.put(id, body.Changes, time.Now().Add(15*time.Minute))
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleTeamsStatus streams how far an apply's reconcile has got, as Server-Sent
// Events, so the page can show it landing rather than a spinner and a guess.
//
//	GET /api/teams/status?id=...
//
// It emits "committed" at once (the change is in the roster's source), then polls
// NSS and emits "applied" the moment the group table reflects every change —
// which is the pipeline, commit → the roster syncs in → usersync converges,
// having actually run. A heartbeat "syncing" goes out between polls. If it does
// not converge before the deadline the stream ends on "syncing"; the change is
// committed regardless and the next read of the page will show it.
func (s *Server) handleTeamsStatus(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.reconcile.get(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	send := func(stage, message string) {
		b, _ := json.Marshal(map[string]any{"stage": stage, "message": message})
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	send("committed", "변경을 기록했습니다. 반영되는 중입니다…")

	ctx := r.Context()
	// Check once straight away: a batch that was all no-ops (or a very fast
	// reconcile) is already applied and should not wait for the first tick.
	if applied, err := s.cfg.Admin.MembershipApplied(ctx, entry.changes); err == nil && applied {
		send("applied", "반영되었습니다.")
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			if applied, err := s.cfg.Admin.MembershipApplied(ctx, entry.changes); err == nil && applied {
				send("applied", "반영되었습니다.")
				return
			}
			send("syncing", "동기화를 기다리는 중입니다…")
		}
	}
}
