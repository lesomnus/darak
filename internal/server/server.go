// Package server is the HTTP face of the file server.
//
// Every file operation goes through the pool as the session's user. The handler
// never decides whether an operation is allowed: it turns the kernel's errno
// into a status code and nothing more. That is what keeps the web path and the
// SMB path on one set of rules instead of two that drift.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/darak/internal/admin"
	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/share"
	"github.com/lesomnus/darak/internal/vfs"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// Config wires a Server.
type Config struct {
	FS   *vfs.FS
	Auth auth.Authenticator

	// Shares issues capability links. Nil disables the feature entirely rather
	// than silently accepting requests it cannot honour.
	Shares *share.Store

	// Admin serves the operator surface. Nil makes every /api/admin route a 404
	// -- the same answer a non-admin gets, so a deployment without it is
	// indistinguishable from one where nobody qualifies.
	Admin *admin.Admin

	// UI, if set, is served at / for anything that is not an API route.
	UI http.Handler

	// SessionTTL bounds how long a login lasts. Sessions are held in memory, so
	// they are also dropped by a restart.
	SessionTTL time.Duration
	// SecureCookies marks the session cookie Secure. It has no default on
	// purpose: silently omitting it in production is worse than making the
	// deployment say which it is.
	SecureCookies bool
	// MaxUpload caps a single request body.
	MaxUpload int64
}

const defaultMaxUpload = 64 << 30 // 64 GiB

// Server serves the HTTP API.
type Server struct {
	cfg      Config
	sessions *Sessions
}

func New(cfg Config) (*Server, error) {
	if cfg.FS == nil || cfg.Auth == nil {
		return nil, errors.New("server: FS and Auth are required")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = defaultMaxUpload
	}
	return &Server{cfg: cfg, sessions: NewSessions(cfg.SessionTTL)}, nil
}

// Sessions exposes the store so a caller can sweep it periodically.
func (s *Server) Sessions() *Sessions { return s.sessions }

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/whoami", s.authed(s.handleWhoami))

	mux.HandleFunc("GET /api/files/", s.authed(s.handleGet))
	mux.HandleFunc("PUT /api/files/", s.authed(s.handlePut))
	mux.HandleFunc("DELETE /api/files/", s.authed(s.handleDelete))
	mux.HandleFunc("POST /api/dirs/", s.authed(s.handleMkdir))

	mux.HandleFunc("POST /api/shares", s.authed(s.handleShareCreate))
	mux.HandleFunc("GET /api/shares", s.authed(s.handleShareList))
	mux.HandleFunc("DELETE /api/shares/", s.authed(s.handleShareRevoke))

	// Every signed-in user may ask whether they are an admin; only an admin may
	// use anything behind it.
	mux.HandleFunc("GET /api/admin/whoami", s.authed(s.handleAdminWhoami))
	mux.HandleFunc("GET /api/admin/users", s.authed(s.adminOnly(s.handleAdminUsers)))
	mux.HandleFunc("GET /api/admin/disk", s.authed(s.adminOnly(s.handleAdminDisk)))
	mux.HandleFunc("GET /api/admin/audit", s.authed(s.adminOnly(s.handleAdminAudit)))
	mux.HandleFunc("POST /api/admin/users/", s.authed(s.adminOnly(s.handleAdminUserOp)))

	// The public side takes no session: the URL is the credential.
	mux.HandleFunc("GET /s/", s.handleSharePublic)
	mux.HandleFunc("POST /s/", s.handleSharePublic)

	if s.cfg.UI != nil {
		mux.Handle("/", s.cfg.UI)
	}
	return mux
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
	writeJSON(w, http.StatusOK, map[string]string{"user": userOf(r)})
}

// --- files ---

// Entry is one row of a directory listing.
type Entry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Mode    string    `json:"mode"`
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

func (s *Server) listDir(w http.ResponseWriter, r *http.Request, user, p string) {
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
	if err := s.cfg.FS.Mkdir(r.Context(), user, p, mode); err != nil {
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
