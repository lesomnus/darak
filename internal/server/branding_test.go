package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/darak/internal/vfs"
)

// brandServer is a server with nothing but a brand. None of these tests touch a
// file route, so the helper pool the rest of the suite stands up is not needed.
func brandServer(t *testing.T, b Brand) http.Handler {
	t.Helper()
	s, err := New(Config{FS: &vfs.FS{}, Auth: fakeAuth{ok: true}, Brand: b})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func get(t *testing.T, h http.Handler, target string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The mark is on the LOGIN page, which is drawn before anyone has signed in. A
// branding route behind the session gate would leave that page unbranded and
// send an operator hunting for a flag that was working all along.
func TestBrandingNeedsNoSession(t *testing.T) {
	h := brandServer(t, Brand{Name: "연구소"})
	rec := get(t, h, "/api/branding")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/branding = %d, want 200", rec.Code)
	}
	var got struct {
		Name string `json:"name"`
		Logo bool   `json:"logo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "연구소" {
		t.Errorf("name = %q, want %q", got.Name, "연구소")
	}
	if got.Logo {
		t.Error("logo = true with no logo configured")
	}
}

func TestBrandingDefaultName(t *testing.T) {
	h := brandServer(t, Brand{})
	rec := get(t, h, "/api/branding")
	if !strings.Contains(rec.Body.String(), DefaultBrandName) {
		t.Errorf("body = %s, want the default name %q", rec.Body, DefaultBrandName)
	}
}

// No logo has to be a 404 rather than a 200 with nothing in it: the interface
// only asks for this URL when /api/branding said there is one, so a 200 here
// would be an empty <img> with a broken-image glyph in the corner of every page.
func TestBrandingLogoAbsentIs404(t *testing.T) {
	h := brandServer(t, Brand{Name: "x"})
	if rec := get(t, h, "/api/branding/logo"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/branding/logo = %d, want 404", rec.Code)
	}
}

func TestBrandingLogoIsServedAndRevalidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mark.png")
	// A real 1x1 PNG, so nothing downstream has to pretend about the bytes.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 40))
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBrand("연구소", path)
	if err != nil {
		t.Fatal(err)
	}
	h := brandServer(t, b)

	rec := get(t, h, "/api/branding/logo")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Body.Len() != len(png) {
		t.Errorf("body = %d bytes, want %d", rec.Body.Len(), len(png))
	}
	// Same reasoning as any user-supplied bytes served same-origin: an SVG that
	// a browser is allowed to sniff or to run is one careless file away from
	// being script on this origin.
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("logo is served without nosniff")
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("logo is served without a CSP")
	}

	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag; every page load would re-send the whole image")
	}
	again := get(t, h, "/api/branding/logo", "If-None-Match", tag)
	if again.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", again.Code)
	}

	// /api/branding must agree that there is one, or the interface never asks.
	if body := get(t, h, "/api/branding").Body.String(); !strings.Contains(body, `"logo":true`) {
		t.Errorf("branding = %s, want logo:true", body)
	}
}

// The logo path comes from a flag, and the whole reason it is read at startup is
// so a mistake in that flag stops the process instead of producing a broken
// image for every visitor. Each of these has to be an error, not a warning.
func TestLoadBrandRejectsBadLogos(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, make([]byte, maxLogoBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	notAnImage := filepath.Join(dir, "mark.txt")
	if err := os.WriteFile(notAnImage, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing":      filepath.Join(dir, "nope.png"),
		"empty":        empty,
		"over the cap": big,
		"not an image": notAnImage,
	} {
		if _, err := LoadBrand("x", path); err == nil {
			t.Errorf("%s: LoadBrand accepted %q", name, path)
		}
	}
}

// An unconfigured logo is the default, not a mistake.
func TestLoadBrandWithoutALogo(t *testing.T) {
	b, err := LoadBrand("", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != DefaultBrandName {
		t.Errorf("name = %q, want %q", b.Name, DefaultBrandName)
	}
	if len(b.Logo) != 0 {
		t.Error("a logo appeared from nowhere")
	}
}
