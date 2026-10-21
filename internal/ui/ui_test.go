package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A stand-in for a Vite build: the document, one script, one stylesheet. The
// real build is not in the repository, so the handler is tested against the
// shape it has rather than against whatever was last built.
var build = fstest.MapFS{
	"index.html": {Data: []byte(`<!doctype html><html><head>` +
		`<script type="module" src="/assets/index-abc123.js"></script>` +
		`<link rel="stylesheet" href="/assets/index-def456.css"></head>` +
		`<body><div id="root"></div></body></html>`)},
	"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
	"assets/index-def456.css": {Data: []byte("body{}")},
	"favicon.svg":             {Data: []byte("<svg/>")},
}

func TestCheck(t *testing.T) {
	if err := Check(build); err != nil {
		t.Fatalf("a well-formed build was refused: %v", err)
	}
	for name, fsys := range map[string]fstest.MapFS{
		"empty":       {},
		"no root":     {"index.html": {Data: []byte(`<script src="/assets/a.js"></script>`)}},
		"no bundle":   {"index.html": {Data: []byte(`<div id="root"></div>`)}},
		"assets only": {"assets/a.js": {Data: []byte("x")}},
	} {
		if err := Check(fsys); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestHandlerServesTheApp(t *testing.T) {
	h := Handler(build)

	// A deep link must return the document rather than 404: the page reads the
	// location itself, so reloading inside a folder has to work.
	for _, path := range []string{"/", "/teams/design", "/homes/alice/sub", "/homes/alice/.trash"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Errorf("GET %s did not return the app document", path)
		}
	}
}

// The file server canonicalises /index.html to /, which is the standard
// behaviour and worth pinning: it is the one path that does NOT fall through to
// the document handler, and a future rewrite could quietly turn it into a 404.
func TestIndexHTMLRedirectsToRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(build).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /index.html = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "./" && loc != "/" {
		t.Errorf("Location = %q, want the root", loc)
	}
}

// Vite puts a content hash in asset names, so those URLs never change meaning
// and can be cached hard. The document must NOT be, or a deploy keeps serving
// the previous bundle to everyone who already has the page.
func TestCachingRules(t *testing.T) {
	h := Handler(build)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("asset = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset Cache-Control = %q, want immutable", cc)
	}

	for _, path := range []string{"/", "/favicon.svg", "/teams/design"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, cc)
		}
	}
}
