package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/auth"
)

// recordingRunner stands in for smbpasswd and keeps what it was handed, so a
// test can check that the password went in on STDIN and never into argv.
type recordingRunner struct {
	mu    sync.Mutex
	stdin string
	args  []string
	err   error
}

func (r *recordingRunner) Run(_ context.Context, stdin, name string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stdin = stdin
	r.args = append([]string{name}, args...)
	return "", r.err
}

// authFunc lets a test vary what the credential store says about the current
// password.
type authFunc func(user, password string) (bool, error)

func (f authFunc) Authenticate(_ context.Context, user, password string) (bool, error) {
	return f(user, password)
}

func passwordServer(t *testing.T, a auth.Authenticator, runner *recordingRunner) *Server {
	t.Helper()
	return &Server{
		cfg: Config{
			Auth:      a,
			Passwords: &auth.PasswordStore{Runner: runner, Path: "smbpasswd"},
		},
		sessions: NewSessions(time.Hour),
	}
}

func change(t *testing.T, s *Server, user, token, current, next string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"current": current, "new": next})
	r := httptest.NewRequest(http.MethodPost, "/api/password", strings.NewReader(string(body)))
	if token != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	w := httptest.NewRecorder()
	s.handlePassword(w, r.WithContext(contextWithUser(r.Context(), user)))
	return w
}

func TestPasswordChange(t *testing.T) {
	runner := &recordingRunner{}
	s := passwordServer(t, authFunc(func(_, password string) (bool, error) {
		return password == "old-password", nil
	}), runner)

	w := change(t, s, "alice", "", "old-password", "a-new-password")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	// Twice, because `smbpasswd -s` reads the new password and its confirmation.
	if runner.stdin != "a-new-password\na-new-password\n" {
		t.Errorf("stdin = %q", runner.stdin)
	}
	// The password must never appear in argv: /proc makes that world-readable,
	// which on a file server means handing it to the very people the
	// permissions exist to separate.
	for _, arg := range runner.args {
		if strings.Contains(arg, "a-new-password") || strings.Contains(arg, "old-password") {
			t.Fatalf("a password reached the command line: %v", runner.args)
		}
	}
	if runner.args[0] != "smbpasswd" {
		t.Errorf("ran %v", runner.args)
	}
}

// A session is a bearer token that lasts twelve hours. One that leaks must not
// be enough to take the account away from the person who owns it.
func TestPasswordChangeRequiresTheCurrentOne(t *testing.T) {
	runner := &recordingRunner{}
	s := passwordServer(t, authFunc(func(_, password string) (bool, error) {
		return password == "old-password", nil
	}), runner)

	w := change(t, s, "alice", "", "guessing", "a-new-password")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", w.Code)
	}
	if runner.stdin != "" {
		t.Error("the password was changed anyway")
	}
}

// "Wrong password" and "cannot ask" have to stay apart, here for the same
// reason they do at login: one sends somebody to reset a password they have not
// forgotten.
func TestPasswordChangeWhenTheStoreCannotBeAsked(t *testing.T) {
	runner := &recordingRunner{}
	s := passwordServer(t, authFunc(func(string, string) (bool, error) {
		return false, auth.ErrUnavailable
	}), runner)

	w := change(t, s, "alice", "", "old-password", "a-new-password")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
	if runner.stdin != "" {
		t.Error("the password was changed without verifying the old one")
	}
}

func TestPasswordRules(t *testing.T) {
	for name, tt := range map[string]struct {
		next string
		want int
	}{
		"too short":       {next: "short", want: http.StatusBadRequest},
		"empty":           {next: "", want: http.StatusBadRequest},
		"carries newline": {next: "line-one\nline-two", want: http.StatusBadRequest},
		"unchanged":       {next: "old-password", want: http.StatusBadRequest},
		"long enough":     {next: "a-new-password", want: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{}
			s := passwordServer(t, authFunc(func(_, password string) (bool, error) {
				return password == "old-password", nil
			}), runner)

			if got := change(t, s, "alice", "", "old-password", tt.next).Code; got != tt.want {
				t.Errorf("status = %d; want %d", got, tt.want)
			}
		})
	}
}

// Somebody who changes their password because they think it was learned expects
// the other sessions to close. It is the only lever they have — SMB holds no
// session, so a client there has to present the new password anyway.
func TestPasswordChangeClosesOtherSessions(t *testing.T) {
	runner := &recordingRunner{}
	s := passwordServer(t, authFunc(func(_, password string) (bool, error) {
		return password == "old-password", nil
	}), runner)

	mine, _ := s.sessions.Create("alice")
	elsewhere, _ := s.sessions.Create("alice")
	somebodyElse, _ := s.sessions.Create("bob")

	w := change(t, s, "alice", mine, "old-password", "a-new-password")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	if _, ok := s.sessions.Lookup(mine); !ok {
		t.Error("the session the change was made from was closed")
	}
	if _, ok := s.sessions.Lookup(elsewhere); ok {
		t.Error("another session of the same person survived")
	}
	if _, ok := s.sessions.Lookup(somebodyElse); !ok {
		t.Error("somebody else's session was closed")
	}
}

// A deployment that cannot reach smbpasswd should not offer the route at all,
// rather than offer it and fail.
func TestPasswordRouteIsAbsentWithoutAStore(t *testing.T) {
	s := &Server{cfg: Config{Auth: fakeAuth{ok: true}}, sessions: NewSessions(time.Hour)}
	if got := change(t, s, "alice", "", "old-password", "a-new-password").Code; got != http.StatusNotFound {
		t.Errorf("status = %d; want 404", got)
	}
}

func TestPasswordStoreRejectsAnImpossibleUser(t *testing.T) {
	runner := &recordingRunner{}
	store := auth.PasswordStore{Runner: runner}
	if err := store.Set(context.Background(), "-rf", "a-new-password"); err == nil {
		t.Fatal("a name that could never be an account reached smbpasswd")
	}
	if runner.args != nil {
		t.Error("smbpasswd was run anyway")
	}
}

func TestPasswordStoreReportsAFailedCommand(t *testing.T) {
	runner := &recordingRunner{err: errors.New("smbpasswd: Failed to find entry for user")}
	store := auth.PasswordStore{Runner: runner}
	if err := store.Set(context.Background(), "alice", "a-new-password"); err == nil {
		t.Fatal("a failed smbpasswd was reported as success")
	}
}
