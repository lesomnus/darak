package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/lesomnus/darak/internal/auth"
)

// Changing your own password.
//
// There is one password per person and it lives in Samba's tdbsam, so this
// changes the SMB password at the same time — they are not two things
// (nas-design.md ADR-2). Nothing here stores or hashes anything: the old one is
// checked by asking ntlm_auth, and the new one is handed to smbpasswd. This
// server keeps no copy of either.
//
// Why the current password is required even though the caller already holds a
// session: a session is a bearer token that lasts twelve hours, and one that
// leaks would otherwise be enough to take the account away from the person who
// owns it. Proving knowledge of the current password is what makes the change
// an act of the account holder rather than of whoever has the cookie.
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Passwords == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	user := userOf(r)

	// Checked before the current password is verified, so somebody who mistypes
	// a too-short new password is told that immediately rather than after a
	// round trip to the credential store.
	if err := auth.CheckPassword(body.New); err != nil {
		writeError(w, http.StatusBadRequest, passwordRuleText(err))
		return
	}
	if body.New == body.Current {
		writeError(w, http.StatusBadRequest, "지금 쓰는 비밀번호와 같습니다.")
		return
	}

	ok, err := s.cfg.Auth.Authenticate(r.Context(), user, body.Current)
	if err != nil {
		// The store could not be asked. Reporting that as a wrong password would
		// send somebody hunting for a password they have not forgotten.
		writeError(w, http.StatusServiceUnavailable, "지금 비밀번호를 확인할 수 없습니다. 잠시 후 다시 시도해 주세요.")
		return
	}
	if !ok {
		// Deliberately the same shape as a failed login. A disabled account also
		// lands here, because ntlm_auth refuses it — which is correct: somebody
		// who may not sign in may not set a new password either.
		slog.Warn("password change refused: current password did not verify", "user", user)
		writeError(w, http.StatusUnauthorized, "지금 쓰는 비밀번호가 맞지 않습니다.")
		return
	}

	if err := s.cfg.Passwords.Set(r.Context(), user, body.New); err != nil {
		if errors.Is(err, auth.ErrWeakPassword) {
			writeError(w, http.StatusBadRequest, passwordRuleText(err))
			return
		}
		slog.Error("could not change a password", "user", user, "err", err)
		writeError(w, http.StatusServiceUnavailable, "비밀번호를 바꾸지 못했습니다. 관리자에게 알려주세요.")
		return
	}

	// Every other session of this person is closed. Somebody changing their
	// password because they think it was learned expects exactly that, and it is
	// the only lever they have: SMB holds no session to close, so a client that
	// wants back in has to present the new password anyway.
	keep := ""
	if c, err := r.Cookie(CookieName); err == nil {
		keep = c.Value
	}
	closed := s.sessions.DeleteOthers(user, keep)

	slog.Info("password changed", "user", user, "sessions_closed", closed)
	writeJSON(w, http.StatusOK, map[string]any{"sessions_closed": closed})
}

// passwordRuleText turns a rule into something worth reading.
//
// The rules are few enough to state in full, which is better than a rejection
// that leaves somebody guessing which of several unstated policies they hit.
func passwordRuleText(err error) string {
	switch {
	case errors.Is(err, auth.ErrWeakPassword):
		return "비밀번호는 " + strconv.Itoa(auth.MinPasswordLength) + "자 이상이어야 하고, 줄바꿈을 넣을 수 없습니다."
	default:
		return "이 비밀번호는 쓸 수 없습니다."
	}
}
