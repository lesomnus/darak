//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/run"
	"github.com/lesomnus/darak/internal/server"
	"github.com/lesomnus/darak/internal/share"
	"github.com/lesomnus/darak/internal/vfs"
)

// --- the ntlm_auth contract, against a real Samba ---

// setupSamba registers an SMB password for a user in a local tdbsam and starts
// winbindd, which is what ntlm_auth talks to.
func setupSamba(t *testing.T, user, password string) {
	t.Helper()

	conf := "/etc/samba/smb.conf"
	if err := os.WriteFile(conf, []byte(
		"[global]\n   workgroup = WORKGROUP\n   security = user\n   passdb backend = tdbsam\n   log level = 0\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("smbpasswd", "-a", "-s", user)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("smbpasswd: %v\n%s", err, out)
	}
	runCmd(t, "smbpasswd", "-e", user)

	// ntlm_auth is a winbind client even on a standalone server; without the
	// daemon it fails outright. This is the operational prerequisite ADR-2 calls
	// out, and it is worth having a test that would notice if it stopped being
	// true.
	_ = exec.Command("pkill", "winbindd").Run()
	if out, err := exec.Command("winbindd", "-D").CombinedOutput(); err != nil {
		t.Fatalf("winbindd: %v\n%s", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("wbinfo", "-p").Run(); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("winbindd did not become ready")
}

// The ntlm-server-1 stdin format is an external contract that was read out of
// documentation. Nothing in the unit tests would notice if it were wrong — they
// assert what this code sends, not what ntlm_auth accepts.
func TestNTLMAuthAgainstRealSamba(t *testing.T) {
	setupWorld(t)
	const (
		user = "alice"
		pw   = "Correct-Horse-9"
	)
	setupSamba(t, user, pw)

	a := auth.NTLM{Runner: run.Exec{}}
	ctx := context.Background()

	ok, err := a.Authenticate(ctx, user, pw)
	if err != nil {
		t.Fatalf("authenticating with the right password: %v", err)
	}
	if !ok {
		t.Error("the correct password was rejected — the helper-protocol format is wrong")
	}

	ok, err = a.Authenticate(ctx, user, pw+"-nope")
	if err != nil {
		t.Fatalf("authenticating with a wrong password should be a verdict, not an error: %v", err)
	}
	if ok {
		t.Error("a wrong password was accepted")
	}

	// A password containing the protocol's own syntax must not be able to steer
	// it. Encoding is what makes that impossible rather than merely unlikely.
	ok, err = a.Authenticate(ctx, user, "x\nalice pw\n")
	if err != nil {
		t.Fatalf("injection attempt: %v", err)
	}
	if ok {
		t.Error("a crafted password authenticated")
	}

	if ok, _ := a.Authenticate(ctx, "nosuchuser", pw); ok {
		t.Error("an unknown user authenticated")
	}
}

// ntlm_auth unescapes both fields unconditionally, so a password containing a
// literal '%' only works if it is encoded on the way in. Reading the
// documentation would not have revealed this; sending one through the real
// helper does.
func TestNTLMAuthWithAwkwardPassword(t *testing.T) {
	setupWorld(t)
	const (
		user = "bob"
		pw   = "50% off & a space: yes"
	)
	setupSamba(t, user, pw)

	ok, err := auth.NTLM{Runner: run.Exec{}}.Authenticate(context.Background(), user, pw)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if !ok {
		t.Error("a password with '%', a space and a colon was rejected — the encoding is wrong")
	}
}

// --- the whole stack ---

type stack struct {
	t   *testing.T
	url string
}

func newStack(t *testing.T) *stack {
	t.Helper()
	setupWorld(t)

	pool, err := helperpool.New(helperpool.Config{
		Bin:  helperBin,
		Root: dataRoot,
		// Re-resolve on every request so a group change is visible immediately;
		// the caching behaviour has its own unit test.
		CredsTTL: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })

	srv, err := server.New(server.Config{
		FS:     &vfs.FS{Pool: pool},
		Auth:   acceptAll{},
		Shares: share.NewStore(),
		// The test client speaks plain HTTP.
		SecureCookies: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &stack{t: t, url: ts.URL}
}

// acceptAll stands in for the credential store. Whether a password is correct is
// covered by TestNTLMAuthAgainstRealSamba; what this stack test is for is that
// the NAME the session carries is the one every file operation runs as.
type acceptAll struct{}

func (acceptAll) Authenticate(context.Context, string, string) (bool, error) { return true, nil }

func (s *stack) client(user string) *http.Client {
	s.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	body := fmt.Sprintf(`{"user":%q,"password":"pw"}`, user)
	resp, err := c.Post(s.url+"/api/login", "application/json", strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("login as %s: %d", user, resp.StatusCode)
	}
	return c
}

func (s *stack) do(c *http.Client, method, path string, body io.Reader) *http.Response {
	s.t.Helper()
	req, err := http.NewRequest(method, s.url+path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp
}

// The end-to-end statement of the whole design: what a user may do over HTTP is
// exactly what they may do on the filesystem, because the request runs as them.
func TestEndToEndPermissions(t *testing.T) {
	s := newStack(t)
	alice := s.client("alice")
	bob := s.client("bob")

	// alice writes in her own home.
	resp := s.do(alice, "PUT", "/api/files/homes/alice/diary.txt", strings.NewReader("private"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice put: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// bob cannot read it, and is told he may not rather than that it is missing.
	resp = s.do(bob, "GET", "/api/files/homes/alice/diary.txt", nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob reading alice's file = %d, want 403; body: %s", resp.StatusCode, got)
	}
	if bytes.Contains(got, []byte("private")) {
		t.Fatal("the response carried the file's contents")
	}

	// bob cannot write there either.
	resp = s.do(bob, "PUT", "/api/files/homes/alice/intrusion.txt", strings.NewReader("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob writing into alice's home = %d, want 403", resp.StatusCode)
	}

	// alice reads her own file back.
	resp = s.do(alice, "GET", "/api/files/homes/alice/diary.txt", nil)
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "private" {
		t.Errorf("alice reading her own file = %d %q", resp.StatusCode, got)
	}
}

// The property the permission layout exists to produce, over HTTP this time.
func TestEndToEndTeamCollaboration(t *testing.T) {
	s := newStack(t)
	alice := s.client("alice")
	bob := s.client("bob")

	resp := s.do(alice, "PUT", "/api/files/teams/team-a/plan.txt", strings.NewReader("alice's draft"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice put: %d", resp.StatusCode)
	}

	// bob replaces it — the file was created by someone else, in a shared folder.
	resp = s.do(bob, "PUT", "/api/files/teams/team-a/plan.txt", strings.NewReader("bob's revision"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob could not overwrite alice's team file: %d", resp.StatusCode)
	}

	resp = s.do(alice, "GET", "/api/files/teams/team-a/plan.txt", nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "bob's revision" {
		t.Errorf("content = %q", got)
	}

	// ...and alice's version is recoverable, which is what makes accepting
	// last-write-wins reasonable in the first place.
	resp = s.do(alice, "GET", "/api/files/teams/team-a/"+vfs.TrashDir, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listing the trash = %d", resp.StatusCode)
	}
	var listing struct {
		Entries []server.Entry `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 {
		t.Fatalf("trash = %v, want alice's replaced version", listing.Entries)
	}
	kept := s.do(alice, "GET", "/api/files/teams/team-a/"+vfs.TrashDir+"/"+listing.Entries[0].Name, nil)
	body, _ := io.ReadAll(kept.Body)
	kept.Body.Close()
	if string(body) != "alice's draft" {
		t.Errorf("recovered %q, want alice's draft", body)
	}
}

// A file uploaded over HTTP has to be indistinguishable from one dropped into
// the SMB share, or "same data, same permission rules" is false exactly where
// somebody would notice.
func TestUploadedFileLooksLikeAnSMBOne(t *testing.T) {
	s := newStack(t)
	alice := s.client("alice")

	resp := s.do(alice, "PUT", "/api/files/teams/team-a/shared.txt", strings.NewReader("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d", resp.StatusCode)
	}

	fi, err := os.Stat(filepath.Join(dataRoot, "teams/team-a/shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o660 {
		t.Errorf("mode = %04o, want 0660", fi.Mode().Perm())
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != aliceUID {
		t.Errorf("owner = %d, want alice (%d) — the write ran as the wrong user", st.Uid, aliceUID)
	}
	if st.Gid != teamGID {
		t.Errorf("group = %d, want the team (%d) — setgid inheritance did not apply", st.Gid, teamGID)
	}
}

func TestEndToEndTraversalIsRefused(t *testing.T) {
	s := newStack(t)
	alice := s.client("alice")

	for _, target := range []string{
		"/api/files/homes/alice/%2e%2e/%2e%2e/etc/shadow",
		"/api/files/../etc/shadow",
	} {
		resp := s.do(alice, "GET", target, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s returned 200 with %d bytes", target, len(body))
		}
		if bytes.Contains(body, []byte("root:")) {
			t.Fatalf("%s leaked /etc/shadow", target)
		}
	}
}

// A share link is the one place the server reads a file for somebody who is not
// the requester. It does it as the OWNER, through the owner's helper, so the
// kernel still decides — the link changes nothing on disk.
func TestEndToEndShareLink(t *testing.T) {
	s := newStack(t)
	alice := s.client("alice")
	bob := s.client("bob")

	resp := s.do(alice, "PUT", "/api/files/homes/alice/private.txt", strings.NewReader("alice only"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d", resp.StatusCode)
	}

	// bob cannot read it at all.
	resp = s.do(bob, "GET", "/api/files/homes/alice/private.txt", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob direct read = %d, want 403", resp.StatusCode)
	}

	// ...until alice hands out a link, which anyone holding can fetch — with no
	// session at all.
	resp = s.do(alice, "POST", "/api/shares",
		strings.NewReader(`{"path":"homes/alice/private.txt","ttl_hours":1}`))
	var link struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&link); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	anonymous := &http.Client{}
	resp = s.do(anonymous, "GET", "/s/"+link.Token, nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "alice only" {
		t.Fatalf("link fetch = %d %q", resp.StatusCode, got)
	}

	// Revoking closes it immediately — the thing a signed URL could not do.
	resp = s.do(alice, "DELETE", "/api/shares/"+link.Token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	resp = s.do(anonymous, "GET", "/s/"+link.Token, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoked link = %d, want 404", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("alice only")) {
		t.Fatal("a revoked link still served the file")
	}
}

// The link is opened as its owner every time, so deleting the file closes it
// with no bookkeeping to remember.
func TestShareLinkDiesWithTheFile(t *testing.T) {
	s := newStack(t)
	alice := s.client("alice")

	resp := s.do(alice, "PUT", "/api/files/homes/alice/tmp.txt", strings.NewReader("here"))
	resp.Body.Close()
	resp = s.do(alice, "POST", "/api/shares",
		strings.NewReader(`{"path":"homes/alice/tmp.txt","ttl_hours":1}`))
	var link struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&link); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp = s.do(alice, "DELETE", "/api/files/homes/alice/tmp.txt", nil)
	resp.Body.Close()

	resp = s.do(&http.Client{}, "GET", "/s/"+link.Token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("link to a deleted file = %d, want 404", resp.StatusCode)
	}
}
