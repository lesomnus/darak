package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
