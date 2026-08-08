package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/lesomnus/darak/internal/share"
)

// unlockSecret authenticates the cookie that says "this browser already gave the
// password". It is per process, so a restart asks again — which is the right
// trade for not having yet another thing to persist.
var unlockSecret = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("server: no entropy for the unlock secret: " + err.Error())
	}
	return b
}()

func unlockCookieName(token string) string {
	// The token is base64url, which is already a legal cookie name.
	return "darak_unlock_" + token
}

// unlockValue proves the password was given for this token.
//
// It has to be unforgeable, not merely present: whoever holds the link already
// has the token, so a cookie they could set themselves would make the password
// decorative.
func unlockValue(token string) string {
	m := hmac.New(sha256.New, unlockSecret)
	m.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func hasUnlock(r *http.Request, token string) bool {
	c, err := r.Cookie(unlockCookieName(token))
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(unlockValue(token)))
}

// --- owner-facing API ---

type shareView struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	Protected bool      `json:"protected"`
}

func (s *Server) viewOf(r *http.Request, l share.Link) shareView {
	return shareView{
		Token:     l.Token,
		URL:       s.shareURL(r, l.Token),
		Path:      l.Path,
		Name:      path.Base(l.Path),
		Created:   l.Created,
		Expires:   l.Expires,
		Protected: l.Protected(),
	}
}

// shareURL builds the absolute link. It is derived from the request rather than
// configured, so a deployment behind a name this process does not know still
// hands out a URL that works.
func (s *Server) shareURL(r *http.Request, token string) string {
	scheme := "https"
	if !s.cfg.SecureCookies && r.TLS == nil {
		scheme = "http"
	}
	if f := r.Header.Get("X-Forwarded-Proto"); f != "" {
		scheme = f
	}
	return scheme + "://" + r.Host + "/s/" + token
}

func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shares == nil {
		writeError(w, http.StatusNotImplemented, "sharing is not enabled")
		return
	}
	var body struct {
		Path     string `json:"path"`
		Password string `json:"password"`
		TTLHours int    `json:"ttl_hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}
	user := userOf(r)

	// Only share something you can actually read, and only a file. Checking by
	// opening it as the user means the kernel answers, exactly as it will on
	// every later fetch — nothing here decides who may share what.
	st, err := s.cfg.FS.Stat(r.Context(), user, body.Path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if st.Mode&0o170000 == 0o040000 {
		writeError(w, http.StatusBadRequest, "a link can only point at a file")
		return
	}
	f, err := s.cfg.FS.Open(r.Context(), user, body.Path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	f.Close()

	l, err := s.cfg.Shares.Create(user, body.Path, body.Password, time.Duration(body.TTLHours)*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create the link")
		return
	}
	writeJSON(w, http.StatusOK, s.viewOf(r, *l))
}

func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shares == nil {
		writeJSON(w, http.StatusOK, map[string]any{"links": []shareView{}})
		return
	}
	links := s.cfg.Shares.ListByOwner(userOf(r))
	out := make([]shareView, 0, len(links))
	for _, l := range links {
		out = append(out, s.viewOf(r, l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shares == nil {
		writeError(w, http.StatusNotImplemented, "sharing is not enabled")
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/api/shares/")
	if err := s.cfg.Shares.Revoke(userOf(r), token); err != nil {
		// Whether someone else's token exists is not a thing to confirm.
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- the public side ---

var unlockPage = template.Must(template.New("unlock").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Protected file</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:22rem;margin:20vh auto;padding:0 1rem;color:#222}
 h1{font-size:1.1rem;margin:0 0 .25rem}
 p{color:#666;margin:0 0 1.25rem}
 input,button{font:inherit;width:100%;box-sizing:border-box;padding:.6rem .7rem;border-radius:6px}
 input{border:1px solid #ccc;margin-bottom:.6rem}
 button{border:0;background:#2a63d0;color:#fff;cursor:pointer}
 .err{color:#b00;margin:0 0 .75rem}
</style></head><body>
<h1>This file is password protected</h1>
<p>{{.Name}}</p>
{{if .Wrong}}<p class="err">That password did not work.</p>{{end}}
<form method="post">
 <input type="password" name="password" placeholder="Password" autofocus required>
 <button type="submit">Open</button>
</form>
</body></html>`))

func (s *Server) handleSharePublic(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shares == nil {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/s/")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	password := ""
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		password = r.PostFormValue("password")
	}

	l, err := s.cfg.Shares.Resolve(token, password)
	switch {
	case errors.Is(err, share.ErrNotFound):
		// Expired, revoked and never-existed all look the same from here.
		http.NotFound(w, r)
		return
	case errors.Is(err, share.ErrPassword):
		// A browser that already answered gets straight through. Get skips the
		// password check, which is safe only because the cookie is an HMAC this
		// process issued — see unlockValue.
		if hasUnlock(r, token) {
			if l2, err2 := s.cfg.Shares.Get(token); err2 == nil {
				l = l2
				break
			}
		}
		s.renderUnlock(w, r, token, r.Method == http.MethodPost)
		return
	case err != nil:
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}

	if l.Protected() && r.Method == http.MethodPost {
		// The password was right: remember it for this browser so a download
		// manager or a Range request does not have to re-post the form.
		http.SetCookie(w, &http.Cookie{
			Name:     unlockCookieName(token),
			Value:    unlockValue(token),
			Path:     "/s/" + token,
			HttpOnly: true,
			Secure:   s.cfg.SecureCookies,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(time.Until(l.Expires).Seconds()),
		})
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}

	s.serveShared(w, r, l)
}

// renderUnlock asks for the link's password.
//
// It deliberately reveals only the file's name: whoever has the URL already
// knows what they were sent, and the path would say which team or whose home it
// came from.
func (s *Server) renderUnlock(w http.ResponseWriter, r *http.Request, token string, wrong bool) {
	name := "Shared file"
	if l, err := s.cfg.Shares.Get(token); err == nil {
		// Only the base name. Whoever has the URL was sent it, so the name tells
		// them nothing new — but the full path would say which team it came from,
		// or whose home.
		name = path.Base(l.Path)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if wrong {
		w.WriteHeader(http.StatusUnauthorized)
	}
	_ = unlockPage.Execute(w, struct {
		Name  string
		Wrong bool
	}{Name: name, Wrong: wrong})
}

// serveShared streams the file AS ITS OWNER.
//
// This is what keeps the link a narrow exception: nothing on disk was changed to
// create it, and the kernel re-checks on every fetch. If the owner loses access
// — dropped from the team, the file deleted — the link stops working on its own,
// with no bookkeeping to remember to do.
func (s *Server) serveShared(w http.ResponseWriter, r *http.Request, l *share.Link) {
	st, err := s.cfg.FS.Stat(r.Context(), l.Owner, l.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.cfg.FS.Open(r.Context(), l.Owner, l.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("ETag", etagOf(st))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A shared link is a URL a stranger may open, so nothing about it should be
	// cached by anything in between.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(path.Base(l.Path)))
	http.ServeContent(w, r, path.Base(l.Path), time.Unix(st.MtimeSec, int64(st.MtimeNsec)).UTC(), f)
}
