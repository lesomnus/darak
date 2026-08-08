package ui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The embedded build is committed, so `go build` needs no Node. If it were ever
// missing the package would not compile at all — but an EMPTY directory embeds
// fine and produces a server that answers every page with nothing, which is a
// far quieter failure.
func TestEmbeddedBuildIsPresent(t *testing.T) {
	sub, err := fs.Sub(content, "dist")
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatalf("no index.html in the embedded build — run scripts/build-ui.sh: %v", err)
	}
	if !strings.Contains(string(index), `id="root"`) {
		t.Errorf("index.html has no mount point:\n%s", index)
	}
	// It must reference the bundle, or the page loads and does nothing.
	if !strings.Contains(string(index), "/assets/") {
		t.Errorf("index.html references no built asset:\n%s", index)
	}

	assets, err := fs.ReadDir(sub, "assets")
	if err != nil || len(assets) == 0 {
		t.Fatalf("the embedded build has no assets — run scripts/build-ui.sh: %v", err)
	}
	var js, css bool
	for _, a := range assets {
		js = js || strings.HasSuffix(a.Name(), ".js")
		css = css || strings.HasSuffix(a.Name(), ".css")
	}
	if !js || !css {
		t.Errorf("expected a script and a stylesheet, got %v", assets)
	}
}

func TestHandlerServesTheApp(t *testing.T) {
	h := Handler()

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
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
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
	sub, _ := fs.Sub(content, "dist")
	assets, err := fs.ReadDir(sub, "assets")
	if err != nil || len(assets) == 0 {
		t.Fatal("no assets to check")
	}

	rec := httptest.NewRecorder()
	h := Handler()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/"+assets[0].Name(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("asset = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset Cache-Control = %q, want immutable", cc)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("document Cache-Control = %q, want no-cache", cc)
	}
}
