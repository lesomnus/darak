// Package server is the HTTP face of the file server.
//
// Every file operation goes through the pool as the session's user. The handler
// never decides whether an operation is allowed: it turns the kernel's errno
// into a status code and nothing more. That is what keeps the web path and the
// SMB path on one set of rules instead of two that drift.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/darak/internal/activity"
	"github.com/lesomnus/darak/internal/admin"
	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/control"
	"github.com/lesomnus/darak/internal/identity"
	"github.com/lesomnus/darak/internal/provision"
	"github.com/lesomnus/darak/internal/share"
	"github.com/lesomnus/darak/internal/sso"
	"github.com/lesomnus/darak/internal/vfs"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// Config wires a Server.
type Config struct {
	FS   *vfs.FS
	Auth auth.Authenticator

	// Passwords changes what tdbsam holds, for somebody changing their own.
	// Nil makes /api/password a 404 — a deployment that cannot reach smbpasswd
	// should not offer the button.
	Passwords *auth.PasswordStore

	// Shares issues capability links. Nil disables the feature entirely rather
	// than silently accepting requests it cannot honour.
	Shares *share.Store

	// Admin serves the operator surface. Nil makes every /api/admin route a 404
	// -- the same answer a non-admin gets, so a deployment without it is
	// indistinguishable from one where nobody qualifies.
	Admin *admin.Admin

	// Activity is the who-changed-what record. Nil reports the feature as off
	// rather than 404, because unlike the admin surface there is nothing to
	// conceal: the caller is already an administrator.
	Activity *activity.Store

	// SSO signs people in through an identity provider. Nil makes every /api/sso
	// route a 404 and the login page draw no button — the password path is the
	// one that is always there.
	SSO *sso.Provider
	// SSOForwardAuth switches the sign-in entry from darak's own code flow to a
	// trusted reverse proxy: the button leads to /api/sso/forward, where the
	// proxy has already authenticated the person and left the verified id_token
	// on the Authorization header. darak still verifies that token (SSO.Assert);
	// the proxy decides WHO, darak decides whether the token is genuine and
	// whether the account may sign in. The code-flow routes stop being used.
	SSOForwardAuth bool
	// Identities translates what the provider asserts into an account name, and
	// Pending holds the identities waiting for an operator to decide about.
	// Both are required when SSO is set, and neither grants anything: what an
	// account may do is still decided by the filesystem, and whether it may sign
	// in at all is decided by Gate.
	Identities *identity.Store
	Pending    *identity.Queue
	// Journal records every change to the mapping. Optional; without it the
	// approvals still work and the history is simply not kept.
	Journal *identity.Journal
	// Gate answers whether an account may open a session. Required when SSO is
	// set: the provider says who somebody is and knows nothing about whether
	// this server still has an account for them.
	Gate AccountGate

	// Controller, when set, is the control plane an unmapped SSO identity is
	// enrolled through: darak Adds an Enrollment (which starts the roster-update
	// pipeline) and reports its Stage back to the person, instead of leaving them
	// a static "waiting for approval" line. Nil keeps the pending-approval queue.
	Controller *control.Controller

	// TrustEmail, when true, binds an existing account to an SSO identity on the
	// first sign-in WITHOUT an operator approval, whenever a trusted-domain
	// address the token carries derives that account's name and the account
	// exists. It removes the rubber-stamp step for members the roster already
	// has, leaving only genuinely new identities for provisioning/approval. It
	// is only sound with a domain allow-list (the provider filters addresses to
	// it), so a deployment that sets this MUST also set the accepted domains;
	// main refuses the combination otherwise.
	TrustEmail bool

	// Provision asks something outside this server to create an account for an
	// identity nothing here answers for yet. Nil leaves every unmapped identity
	// to the approval queue, which is the default and the conservative one.
	Provision *provision.Provisioner
	// ProvisionConfig reports the rules currently in force, for the operator
	// page. Read-only by construction: the rules are a deployed file, and a page
	// that could edit them would be a way to grant yourself an account.
	ProvisionConfig func() provision.Status

	// UI, if set, is served at / for anything that is not an API route.
	UI http.Handler

	// Brand is what the interface puts in its corner. The zero value is the
	// built-in mark and DefaultBrandName; see LoadBrand.
	Brand Brand

	// SessionTTL bounds how long a login lasts. Sessions are held in memory, so
	// they are also dropped by a restart.
	SessionTTL time.Duration
	// SecureCookies marks the session cookie Secure. It has no default on
	// purpose: silently omitting it in production is worse than making the
	// deployment say which it is.
	SecureCookies bool

	// AnonymousUser is the OS account file requests without a session are served
	// as. Empty (the default) disables anonymous access entirely: no session,
	// no answer, exactly as before. When set, an unauthenticated file request is
	// run as this account instead of refused — and because that account is a
	// member of no group, the kernel confines it to world-accessible (public)
	// folders and nothing else. darak makes no access decision here; it never
	// does. The account must exist in NSS, have NO SMB (tdbsam) credential — so
	// anonymous access can never reach the SMB path — and belong to no group.
	AnonymousUser string
	// MaxUpload caps a single request body.
	MaxUpload int64
}

const defaultMaxUpload = 64 << 30 // 64 GiB

// Server serves the HTTP API.
type Server struct {
	cfg       Config
	sessions  *Sessions
	flows     *sso.Flows
	notices   *notices
	enroll    *enrollTracker
	reconcile *reconcileTracker
}

func New(cfg Config) (*Server, error) {
	if cfg.FS == nil || cfg.Auth == nil {
		return nil, errors.New("server: FS and Auth are required")
	}
	// Half a sign-on configuration is worse than none: a provider with no way to
	// resolve what it asserts would authenticate people into nothing, and one
	// with no gate would let an approval outlive the account it points at.
	if cfg.SSO != nil && (cfg.Identities == nil || cfg.Pending == nil || cfg.Gate == nil) {
		return nil, errors.New("server: SSO needs Identities, Pending and Gate")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = defaultMaxUpload
	}
	if cfg.Brand.Name == "" {
		cfg.Brand.Name = DefaultBrandName
	}
	return &Server{
		cfg:       cfg,
		sessions:  NewSessions(cfg.SessionTTL),
		flows:     sso.NewFlows(),
		notices:   newNotices(),
		enroll:    newEnrollTracker(),
		reconcile: newReconcileTracker(),
	}, nil
}

// Sessions exposes the store so a caller can sweep it periodically.
func (s *Server) Sessions() *Sessions { return s.sessions }

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	// whoami answers for the anonymous visitor too, so the UI can tell it is
	// browsing anonymously and offer a sign-in rather than a locked door.
	mux.HandleFunc("GET /api/whoami", s.authedOrAnon(s.handleWhoami))
	// The public folders an anonymous visitor may browse without signing in.
	// Reads the roster's `anonymous` levels; 404 without an Admin to read it.
	mux.HandleFunc("GET /api/public", s.authedOrAnon(s.handlePublicFolders))
	// Behind authed, and it still asks for the current password: a session is a
	// bearer token, and one that leaks must not be enough to take an account
	// away from the person who owns it.
	mux.HandleFunc("POST /api/password", s.authed(s.handlePassword))

	// The sign-on routes take no session — they are how one is obtained — and
	// answer 404 when no provider is configured, so a deployment without it looks
	// like a build without it.
	mux.HandleFunc("GET /api/sso/login", s.handleSSOLogin)
	mux.HandleFunc("GET /api/sso/callback", s.handleSSOCallback)
	mux.HandleFunc("GET /api/sso/forward", s.handleSSOForward)
	mux.HandleFunc("GET /api/sso/notice", s.handleSSONotice)
	// Following an onboarding: the id in the notice is the capability, so no
	// session, exactly like the notice it came in.
	mux.HandleFunc("GET /api/sso/enrollment", s.handleSSOEnrollment)

	// Not behind authed(): the login page carries the mark too, and it is drawn
	// before anyone has signed in.
	mux.HandleFunc("GET /api/branding", s.handleBranding)
	mux.HandleFunc("GET /api/branding/logo", s.handleBrandingLogo)

	// File operations admit the anonymous visitor; the kernel then decides what
	// that group-less account can actually touch (read anywhere world-readable,
	// write/create/delete only where world-writable — i.e. the public folders).
	mux.HandleFunc("GET /api/files/", s.authedOrAnon(s.handleGet))
	mux.HandleFunc("PUT /api/files/", s.authedOrAnon(s.handlePut))
	mux.HandleFunc("DELETE /api/files/", s.authedOrAnon(s.handleDelete))
	mux.HandleFunc("POST /api/dirs/", s.authedOrAnon(s.handleMkdir))
	mux.HandleFunc("GET /api/mode/", s.authedOrAnon(s.handleModeInfo))
	// Changing a mode is an ownership act, not a file op: kept to signed-in users
	// so an anonymous visitor cannot re-permission a public folder's contents.
	mux.HandleFunc("POST /api/mode/", s.authed(s.handleChmod))
	mux.HandleFunc("GET /api/search/", s.authedOrAnon(s.handleSearch))

	mux.HandleFunc("POST /api/shares", s.authed(s.handleShareCreate))
	mux.HandleFunc("GET /api/shares", s.authed(s.handleShareList))
	mux.HandleFunc("DELETE /api/shares/", s.authed(s.handleShareRevoke))

	// Every signed-in user may ask whether they are an admin; only an admin may
	// use anything behind it.
	mux.HandleFunc("GET /api/admin/whoami", s.authed(s.handleAdminWhoami))
	mux.HandleFunc("GET /api/admin/users", s.authed(s.adminOnly(s.handleAdminUsers)))
	mux.HandleFunc("GET /api/admin/disk", s.authed(s.adminOnly(s.handleAdminDisk)))
	mux.HandleFunc("GET /api/admin/audit", s.authed(s.adminOnly(s.handleAdminAudit)))
	mux.HandleFunc("GET /api/admin/activity", s.authed(s.adminOnly(s.handleActivity)))
	mux.HandleFunc("GET /api/admin/users/", s.authed(s.adminOnly(s.handleAdminUserRead)))
	mux.HandleFunc("POST /api/admin/users/", s.authed(s.adminOnly(s.handleAdminUserOp)))

	// The identity mapping is an operator surface for the same reason resetting
	// an SMB password is: it changes who can get in without touching the roster,
	// which is the boundary internal/admin/ops.go draws.
	mux.HandleFunc("GET /api/admin/identities", s.authed(s.adminOnly(s.ssoOnly(s.handleIdentityList))))
	mux.HandleFunc("GET /api/admin/identities/journal", s.authed(s.adminOnly(s.ssoOnly(s.handleIdentityJournal))))
	mux.HandleFunc("GET /api/admin/provisioning", s.authed(s.adminOnly(s.ssoOnly(s.handleProvisioning))))
	mux.HandleFunc("POST /api/admin/identities", s.authed(s.adminOnly(s.ssoOnly(s.handleIdentityApprove))))
	mux.HandleFunc("DELETE /api/admin/identities/pending", s.authed(s.adminOnly(s.ssoOnly(s.handleIdentityDiscard))))
	mux.HandleFunc("DELETE /api/admin/identities/", s.authed(s.adminOnly(s.ssoOnly(s.handleIdentityForget))))

	// Team ownership is a separate axis from the admin group: an owner may
	// change their own team's membership and nothing else, so these are gated
	// per-team inside the handler rather than by adminOnly.
	mux.HandleFunc("GET /api/teams", s.authed(s.handleTeams))
	mux.HandleFunc("GET /api/teams/whoami", s.authed(s.handleTeamWhoami))
	// More specific than the /api/teams/ prefix below, so these win for their
	// paths: apply lands a batch of changes, status streams the reconcile it
	// started. status is a cookie-authed SSE stream (EventSource carries it).
	mux.HandleFunc("POST /api/teams/apply", s.authed(s.handleTeamsApply))
	mux.HandleFunc("GET /api/teams/status", s.authed(s.handleTeamsStatus))
	mux.HandleFunc("POST /api/teams/", s.authed(s.handleTeamMembers))

	// The public side takes no session: the URL is the credential.
	mux.HandleFunc("GET /s/", s.handleSharePublic)
	mux.HandleFunc("POST /s/", s.handleSharePublic)

	if s.cfg.UI != nil {
		mux.Handle("/", s.cfg.UI)
	}
	// Outermost, so it sees the Content-Type every handler below it settled on
	// and can decide from that rather than from the route.
	return Compress(mux)
}

// --- session plumbing ---

type ctxKey int

const userKey ctxKey = 0

// userOf returns the authenticated user for a request.
//
// It reads only from the session. The user is never taken from a header, a query
// parameter or the path: those are supplied by the caller, and every permission
// decision downstream is made by running as whoever this says.
func userOf(r *http.Request) string {
	u, _ := r.Context().Value(userKey).(string)
	return u
}

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		user, ok := s.sessions.Lookup(c.Value)
		if !ok {
			clearCookie(w, s.cfg.SecureCookies)
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		ctx := contextWithUser(r.Context(), user)
		next(w, r.WithContext(ctx))
	}
}

// authedOrAnon is authed with an anonymous fallback. A request carrying a valid
// session is served as that user, exactly as authed. A request WITHOUT one is,
// when AnonymousUser is configured, served as that account instead of refused —
// so an unauthenticated visitor can read (and, where the folder's mode allows,
// write) the public folders, and only those, because the kernel confines the
// group-less anonymous account to world-accessible paths. With no AnonymousUser
// set it behaves exactly like authed's 401. The user still comes only from the
// session or this fixed account — never from a header, query or path.
func (s *Server) authedOrAnon(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil {
			if user, ok := s.sessions.Lookup(c.Value); ok {
				next(w, r.WithContext(contextWithUser(r.Context(), user)))
				return
			}
		}
		if s.cfg.AnonymousUser == "" {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next(w, r.WithContext(contextWithUser(r.Context(), s.cfg.AnonymousUser)))
	}
}

// isAnon reports whether the request is being served as the anonymous account
// rather than a signed-in user.
func (s *Server) isAnon(r *http.Request) bool {
	return s.cfg.AnonymousUser != "" && userOf(r) == s.cfg.AnonymousUser
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}

	ok, err := s.cfg.Auth.Authenticate(r.Context(), body.User, body.Password)
	if err != nil {
		// The credential store could not be asked. Answering "wrong password"
		// would make every user look like they had forgotten theirs at once and
		// send whoever is on call to the wrong system.
		writeError(w, http.StatusServiceUnavailable, "cannot verify credentials right now")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, err := s.sessions.Create(body.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start a session")
		return
	}
	setCookie(w, token, s.cfg.SessionTTL, s.cfg.SecureCookies)
	writeJSON(w, http.StatusOK, map[string]string{"user": body.User})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		s.sessions.Delete(c.Value)
	}
	clearCookie(w, s.cfg.SecureCookies)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": userOf(r), "anonymous": s.isAnon(r)})
}

// --- files ---

// Entry is one row of a directory listing.
type Entry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Mode    string    `json:"mode"`
	// Accessible, when set, says whether this user may enter the folder — filled
	// only for the `teams` root, from the roster, so the interface can lock the
	// teams a person cannot open instead of letting them find out by clicking.
	// Nil means "not computed" (every listing but the teams root), never "no".
	Accessible *bool `json:"accessible,omitempty"`
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/files/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}

	st, err := s.cfg.FS.Stat(r.Context(), user, p)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		s.listDir(w, r, user, p)
		return
	}
	s.serveFile(w, r, user, p, st)
}

// listPublicTeams answers the anonymous listing of the `teams` root with only
// the folders the roster declares public, presented as ordinary directory
// entries. Without an Admin to read the roster it lists nothing rather than
// falling back to the real directory — the point is precisely not to enumerate
// it. The kernel still gates entry into each one.
func (s *Server) listPublicTeams(w http.ResponseWriter, r *http.Request) {
	out := []Entry{}
	if s.cfg.Admin != nil {
		if folders, err := s.cfg.Admin.PublicFolders(r.Context()); err == nil {
			for _, f := range folders {
				out = append(out, Entry{Name: f.Name, Dir: true})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": "teams", "entries": out})
}

func (s *Server) listDir(w http.ResponseWriter, r *http.Request, user, p string) {
	// An anonymous visitor must not be able to enumerate the top of the tree:
	// listing `teams` or `homes` directly would leak every team and account name,
	// even though the folders themselves stay closed to it. So for the anonymous
	// identity those two roots are answered from the roster's public set (for
	// `teams`) or as empty (`homes`). Deeper paths are untouched, and the kernel
	// still guards what the anonymous account may actually open.
	if s.isAnon(r) {
		switch path.Clean(p) {
		case "teams":
			s.listPublicTeams(w, r)
			return
		case "homes":
			writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": []Entry{}})
			return
		}
	}

	ents, err := s.cfg.FS.ReadDir(r.Context(), user, p, true)
	if err != nil {
		writeFSError(w, err)
		return
	}
	out := make([]Entry, 0, len(ents))
	for _, e := range ents {
		// A crashed upload should not show up as a mysterious zero-byte file the
		// user has no idea how to get rid of. The trash IS listed: restoring from
		// it is the point of keeping it.
		if vfs.IsTempName(e.Name) {
			continue
		}
		row := Entry{Name: e.Name, Dir: e.Type == unix.DT_DIR}
		if e.Stat != nil {
			row.Size = e.Stat.Size
			row.ModTime = time.Unix(e.Stat.MtimeSec, int64(e.Stat.MtimeNsec)).UTC()
			row.Mode = fmt.Sprintf("%04o", e.Stat.Mode&0o7777)
			row.Dir = e.Stat.Mode&unix.S_IFMT == unix.S_IFDIR
		}
		out = append(out, row)
	}
	// At the teams root, mark which teams this user may actually enter — computed
	// from the roster (one read), not by probing each folder. The kernel still
	// guards entry; this only lets the interface show a lock instead of producing
	// a permission error on the click.
	if !s.isAnon(r) && s.cfg.Admin != nil && path.Clean(p) == "teams" {
		if access, err := s.cfg.Admin.TeamAccess(r.Context(), user); err == nil {
			for i := range out {
				if v, ok := access[out[i].Name]; ok {
					out[i].Accessible = &v
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": out})
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, user, p string, st *wire.Stat) {
	f, err := s.cfg.FS.Open(r.Context(), user, p)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer f.Close()

	// The descriptor is a ReadSeeker, so ServeContent handles Range, If-Range and
	// If-Modified-Since. Reimplementing byte ranges is a classic source of
	// off-by-one bugs, and video scrubbing and resumed downloads depend on it.
	modTime := time.Unix(st.MtimeSec, int64(st.MtimeNsec)).UTC()
	w.Header().Set("ETag", etagOf(st))
	// Content is user data; a browser must not be talked into running it.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(path.Base(p)))
	http.ServeContent(w, r, path.Base(p), modTime, f)
}

// etagOf identifies a specific version of a file.
//
// inode, size and mtime together change whenever the content does — including
// across the write protocol's rename, which publishes a different inode, so a
// cached copy is never mistaken for the new version.
func etagOf(st *wire.Stat) string {
	return fmt.Sprintf(`"%x-%x-%x.%x"`, st.Ino, st.Size, st.MtimeSec, st.MtimeNsec)
}

// inheritOtherBits widens a new object's mode with the "other" (world) bits of
// its parent directory, so content created under a public folder is as reachable
// to the world as the folder itself: a file becomes world-readable under an
// anonymous:read folder and world-writable under anonymous:write, while under a
// private folder (no other bits) nothing changes. A file never inherits the
// execute bit; a directory inherits all three.
//
// The parent's on-disk mode is the single source of truth — usersync set it
// from the roster — so this needs no roster lookup and can never disagree with
// what the kernel actually enforces. It also keeps the web and SMB paths on one
// rule: smbd's public masks (0664/0666, 2775/2777) reproduce exactly this.
//
// A parent that cannot be stat'd (gone, or unreadable to this user) leaves the
// base mode untouched; the create that follows fails on its own if it must.
func (s *Server) inheritOtherBits(ctx context.Context, user, p string, mode uint32, dir bool) uint32 {
	st, err := s.cfg.FS.Stat(ctx, user, path.Dir(p))
	if err != nil {
		return mode
	}
	return widenOther(mode, st.Mode, dir)
}

// widenOther folds the "other" bits of parentMode into mode. A directory takes
// all three (r, w, x); a plain file takes read and write but never execute — a
// file is not executable just because the folder holding it is traversable.
func widenOther(mode, parentMode uint32, dir bool) uint32 {
	other := parentMode & 0o007
	if !dir {
		other &^= 0o001
	}
	return mode | other
}

// handlePublicFolders lists the folders open to anonymous visitors, from the
// roster's `anonymous` levels. It powers the anonymous landing page (what can I
// look at without signing in) and is harmless to a signed-in user too. Without
// an Admin to read the roster there is nothing to list, so it 404s like every
// other roster-backed surface.
func (s *Server) handlePublicFolders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Admin == nil {
		writeError(w, http.StatusNotFound, "not available")
		return
	}
	folders, err := s.cfg.Admin.PublicFolders(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not read the roster: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/files/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}
	mode, err := vfs.CreateMode(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode = s.inheritOtherBits(r.Context(), user, p, mode, false)

	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	defer body.Close()
	if err := s.cfg.FS.Write(r.Context(), user, p, body, mode); err != nil {
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/files/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}
	if err := s.cfg.FS.Remove(r.Context(), user, p); err != nil {
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/dirs/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}
	mode, err := vfs.DirMode(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode = s.inheritOtherBits(r.Context(), user, p, mode, true)
	if err := s.cfg.FS.Mkdir(r.Context(), user, p, mode); err != nil {
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleModeInfo answers what the mode dialog needs to warn accurately.
//
// GET /api/mode/<path> -> {"mode":"0640","dir":false,"acl":true}
//
// `acl` is the reason this is a request and not just a field the client already
// has from the listing: whether a file carries a POSIX ACL is not in its mode
// bits, and narrowing the group bits can silently mask a reader's access out.
// The warning can only be concrete — "this file has extra permissions a change
// may hide" — if the server looks.
func (s *Server) handleModeInfo(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/mode/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}
	st, err := s.cfg.FS.Stat(r.Context(), user, p)
	if err != nil {
		writeFSError(w, err)
		return
	}
	acl, err := s.cfg.FS.HasACL(r.Context(), user, p)
	if err != nil {
		// The mode is still worth returning; the ACL hint just degrades to
		// unknown-so-warn-generically rather than failing the dialog.
		acl = false
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": fmt.Sprintf("%04o", st.Mode&0o7777),
		"dir":  st.Mode&unix.S_IFMT == unix.S_IFDIR,
		"acl":  acl,
	})
}

// handleChmod changes a file's permission bits.
//
// POST /api/mode/<path>  {"mode": "0640"}
//
// Octal as a STRING. A JSON number would arrive as 640 decimal and mean 1200
// octal, which is the kind of bug that silently makes a file setuid — and
// `"0640"` is also how a person writes a mode, so the wire format matches what
// the operator typed.
//
// Nothing here decides whether the change is permitted. chmod succeeds for the
// file's owner and nobody else, and the helper runs as the session's user, so
// the kernel answers exactly as it does for every other operation. What IS here
// is one refusal that is not about permission at all; see below.
func (s *Server) handleChmod(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/mode/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mode, err := strconv.ParseUint(strings.TrimSpace(body.Mode), 8, 32)
	if err != nil || mode > 0o7777 {
		writeError(w, http.StatusBadRequest, `mode는 8진수 문자열이어야 합니다 (예: "0640")`)
		return
	}

	st, err := s.cfg.FS.Stat(r.Context(), user, p)
	if err != nil {
		writeFSError(w, err)
		return
	}
	isDir := st.Mode&unix.S_IFMT == unix.S_IFDIR

	// A team directory that loses setgid keeps working for everything already in
	// it and quietly breaks everything created afterwards: new files get the
	// creator's own group instead of the team's, so their colleagues cannot open
	// them. Nothing fails at the time, and the two events are weeks apart.
	//
	// This is not a permission decision — the kernel would allow it — it is a
	// refusal to let one click break an invariant the rest of the system rests
	// on. Clearing it deliberately is still possible over SMB or a shell.
	if isDir && mode&unix.S_ISGID == 0 {
		if domain, err := vfs.DomainRoot(p); err == nil && strings.HasPrefix(domain, "teams/") {
			writeError(w, http.StatusConflict,
				"팀 폴더에서 setgid(2xxx)를 빼면 이후 만들어지는 파일이 팀 그룹을 갖지 못합니다. "+
					"2770처럼 앞에 2를 붙여 주세요.")
			return
		}
	}

	if err := s.cfg.FS.Chmod(r.Context(), user, p, uint32(mode)); err != nil {
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// requestPath strips the route prefix. It does not clean or validate the
// remainder: openat2 with RESOLVE_BENEATH decides what a path means, in the
// helper, with the user's credentials. A check here would be a second, weaker
// answer to a question the kernel already answers.
func requestPath(r *http.Request, prefix string) string {
	return strings.TrimPrefix(r.URL.Path, prefix)
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c), 16)))
	}
	return b.String()
}

// decodeBody reads a small JSON request body, answering 400 itself when it
// cannot. It reports whether the caller should carry on.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeFSError maps a kernel verdict to a status code.
//
// The mapping is the only place the server interprets a permission result, and
// it interprets it into a number — it never overrides one. EACCES is 403 even
// when the caller might prefer 404: pretending a file the user cannot reach does
// not exist would be a second, invented rule about who may know what.
func writeFSError(w http.ResponseWriter, err error) {
	var e *vfs.Errno
	if !errors.As(err, &e) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	switch e.Err {
	case unix.ENOENT, unix.ENOTDIR:
		writeError(w, http.StatusNotFound, "not found")
	case unix.EACCES, unix.EPERM:
		writeError(w, http.StatusForbidden, "not permitted")
	case unix.EEXIST:
		writeError(w, http.StatusConflict, "already exists")
	case unix.EISDIR:
		writeError(w, http.StatusBadRequest, "is a directory")
	case unix.ENOTEMPTY:
		writeError(w, http.StatusConflict, "directory is not empty")
	case unix.EXDEV:
		// RESOLVE_BENEATH answers this way when a path would leave the root.
		writeError(w, http.StatusBadRequest, "path is outside the served tree")
	case unix.ENOSPC, unix.EDQUOT:
		writeError(w, http.StatusInsufficientStorage, "out of space")
	case unix.ENAMETOOLONG:
		writeError(w, http.StatusBadRequest, "name too long")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
