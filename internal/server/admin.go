package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/darak/internal/activity"
	"github.com/lesomnus/darak/internal/admin"
)

// adminOnly gates a handler on membership in the admin group.
//
// It runs INSIDE authed, so the name it checks is the session's and never the
// request's. And it re-resolves the group on every call rather than stamping a
// flag into the session at login: a session lasts twelve hours, so a
// login-time decision would keep an ex-admin authorized for the rest of the
// day after their membership was removed.
//
// A caller who is not an admin gets 404, not 403. There is nothing to gain from
// confirming to a signed-in non-admin that an admin API exists at this path.
func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Admin == nil {
			http.NotFound(w, r)
			return
		}
		ok, err := s.cfg.Admin.IsAdmin(r.Context(), userOf(r))
		if err != nil {
			// Failing closed. A name service that cannot answer "is this person
			// an admin" is not permission to assume they are.
			writeError(w, http.StatusServiceUnavailable, "cannot check administrator access right now")
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// handleWhoamiAdmin is how the interface knows whether to offer the page. It is
// authed but NOT adminOnly: every signed-in user asks it, and the answer for
// most of them is false.
//
// It is a hint for rendering and nothing more — the actual gate is adminOnly on
// each route, so a caller who lies to their own browser gains nothing.
func (s *Server) handleAdminWhoami(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Admin == nil {
		writeJSON(w, http.StatusOK, map[string]any{"admin": false, "group": ""})
		return
	}
	ok, err := s.cfg.Admin.IsAdmin(r.Context(), userOf(r))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"admin": false, "group": s.cfg.Admin.Group()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"admin": ok, "group": s.cfg.Admin.Group()})
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	inv, err := s.cfg.Admin.Inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not read the accounts: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) handleAdminDisk(w http.ResponseWriter, r *http.Request) {
	cap, err := s.cfg.Admin.Capacity()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not read the filesystem: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capacity": cap,
		"usage":    s.cfg.Admin.Usage(),
	})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	rep, err := s.cfg.Admin.Audit(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not run the audit: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleAdminInitialPassword shows the seed-derived initial password for one
// user, so onboarding does not need a shell on the node.
//
// GET /api/admin/users/<name>/initial-password
//
// It is VERIFIED before it is shown. usersync recomputes the same value forever
// — it has no idea whether the person has since changed theirs — so handing it
// over unchecked means occasionally telling somebody their password is
// something it is not, which presents as a broken login rather than as a stale
// note. Asking the credential store first makes the answer either true or
// absent.
//
// What this deliberately does NOT do is show a current password. tdbsam holds
// an NT hash; there is nothing to show. For somebody who has changed theirs the
// only route is a reset, which is the button next to this one.
func (s *Server) handleAdminInitialPassword(w http.ResponseWriter, r *http.Request, name string) {
	password, err := s.cfg.Admin.InitialPassword(r.Context(), name)
	if err != nil {
		if errors.Is(err, admin.ErrUnknownUser) {
			writeError(w, http.StatusNotFound, "관리 대상 계정이 아닙니다")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "초기 비밀번호를 계산할 수 없습니다: "+err.Error())
		return
	}

	ok, err := s.cfg.Auth.Authenticate(r.Context(), name, password)
	if err != nil {
		// Unverifiable. Showing it anyway would be showing something that may
		// already be wrong, which is the failure this check exists to prevent.
		writeError(w, http.StatusServiceUnavailable, "지금 확인할 수 없습니다. 잠시 후 다시 시도해 주세요.")
		return
	}

	// Disclosure of a credential, by name, on request. Logged for that reason —
	// it is the one thing on this page that hands over a way in rather than
	// changing one.
	slog.Warn("initial password revealed", "by", userOf(r), "user", name, "still_initial", ok)

	// The value is a credential; it must not sit in a proxy or a browser cache.
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"still_initial": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"still_initial": true, "password": password})
}

// handleAdminUserRead routes the read-only per-user operations.
func (s *Server) handleAdminUserRead(w http.ResponseWriter, r *http.Request) {
	rest := requestPath(r, "/api/admin/users/")
	name, op, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		writeError(w, http.StatusBadRequest, "expected /api/admin/users/<user>/initial-password")
		return
	}
	switch op {
	case "initial-password":
		s.handleAdminInitialPassword(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

// handleAdminUserOp performs one of the roster-preserving account operations.
//
// POST /api/admin/users/<name>/smb   {"enabled": bool}
// POST /api/admin/users/<name>/password  {"password": "..."}
func (s *Server) handleAdminUserOp(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
	name, op, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		writeError(w, http.StatusBadRequest, "expected /api/admin/users/<user>/<smb|password>")
		return
	}

	var body struct {
		Enabled  *bool  `json:"enabled"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	var err error
	switch op {
	case "smb":
		if body.Enabled == nil {
			writeError(w, http.StatusBadRequest, "expected {\"enabled\": true|false}")
			return
		}
		// Locking yourself out is an accident, not an operation. The page has no
		// button for it either, but the API is what has to refuse.
		if !*body.Enabled && name == userOf(r) {
			writeError(w, http.StatusConflict, "refusing to disable your own account")
			return
		}
		err = s.cfg.Admin.SetSMBEnabled(r.Context(), name, *body.Enabled)
	case "password":
		err = s.cfg.Admin.SetSMBPassword(r.Context(), name, body.Password)
	default:
		writeError(w, http.StatusNotFound, "unknown operation "+op)
		return
	}

	switch {
	case errors.Is(err, admin.ErrUnknownUser):
		writeError(w, http.StatusNotFound, "no such managed account")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- team membership ---
//
// A different gate from adminOnly. The operator page is for administrators; a
// team owner is not one and must not reach the rest of it, so these routes
// authorize per-team rather than per-role.

// handleTeamWhoami tells a signed-in user which teams they may manage. Authed
// but not gated: the answer for most people is an empty list, and it is what
// lets the interface decide whether to offer anything at all.
func (s *Server) handleTeamWhoami(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Admin == nil {
		writeJSON(w, http.StatusOK, map[string]any{"teams": []string{}})
		return
	}
	teams, err := s.cfg.Admin.OwnedTeams(r.Context(), userOf(r))
	if err != nil {
		// Not an error to the caller: they own nothing we can confirm right now,
		// and every route behind this re-checks anyway.
		writeJSON(w, http.StatusOK, map[string]any{"teams": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}

// handleTeams lists the teams the caller may change, with their membership.
// Drives the team panel for an owner, who cannot read the full inventory.
func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Admin == nil {
		writeJSON(w, http.StatusOK, map[string]any{"teams": []any{}, "users": []string{}})
		return
	}
	view, err := s.cfg.Admin.ManageableTeams(r.Context(), userOf(r))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not read the teams: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleTeamMembers changes one membership.
//
//	POST /api/teams/<team>/members  {"user": "...", "member": true|false}
func (s *Server) handleTeamMembers(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Admin == nil {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/teams/")
	team, tail, ok := strings.Cut(rest, "/")
	if !ok || tail != "members" || team == "" {
		writeError(w, http.StatusBadRequest, "expected /api/teams/<team>/members")
		return
	}

	var body struct {
		User   string `json:"user"`
		Member *bool  `json:"member"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Member == nil {
		writeError(w, http.StatusBadRequest, "expected {\"user\": \"...\", \"member\": true|false}")
		return
	}

	err := s.cfg.Admin.SetTeamMembership(r.Context(), userOf(r), team, body.User, *body.Member)
	switch {
	case errors.Is(err, admin.ErrNotOwner):
		// 404, like the admin routes: a signed-in stranger learns nothing about
		// which teams exist or who owns them.
		http.NotFound(w, r)
	case errors.Is(err, admin.ErrUnknownUser):
		writeError(w, http.StatusNotFound, "no such managed account")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- activity ---

// handleActivity answers "who touched this file".
//
// Admin-gated. The record names who did what, and the people it names have no
// business reading it — a shared team folder would otherwise let anyone see
// every colleague's working pattern.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Activity == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}, "enabled": false})
		return
	}
	q := r.URL.Query()
	query := activity.Query{
		User:   q.Get("user"),
		Path:   q.Get("path"),
		Action: activity.Action(q.Get("action")),
	}
	if v := q.Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query.Since = time.Now().AddDate(0, 0, -n)
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			query.Limit = n
		}
	}

	events, err := s.cfg.Activity.Query(query)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not read the activity log: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "enabled": true})
}
