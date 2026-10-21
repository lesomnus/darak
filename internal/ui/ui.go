// Package ui serves the browser interface, from a directory, as an unprivileged
// process.
//
// The interface is written in TypeScript and React under web/ and built by Vite.
// The build output is NOT committed and NOT embedded: the image builds it in its
// own stage and puts it beside the binary, and a deployment that ships the
// interface separately points `darak ui` at wherever it landed.
//
// This is the one package outside the helper allowed to resolve a path
// (internal/lint), and what earns it that is Open: it refuses to run as root.
// The server never imports it. It reaches the interface over HTTP, through
// internal/uiproc, so the process that opens these files is never the one whose
// permission checks always pass.
//
// The interface is the REQUIRED path (nas-design.md §2): the people it is for do
// not mount SMB shares, so anything they cannot do here they cannot do at all.
package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// Open returns the built interface in dir.
//
// It fails rather than serving nothing: an empty or mistyped directory would
// otherwise produce a server that answers every page with a 404 or a blank
// document, which is a far quieter failure than not starting.
func Open(dir string) (fs.FS, error) {
	// Running as root would turn every path this resolves into one the kernel
	// does not check. The API server is the process that needs root; this one is
	// the process that must not have it.
	if os.Geteuid() == 0 {
		return nil, errors.New("ui: refusing to serve as root; run `darak ui` as an unprivileged user")
	}
	fsys := os.DirFS(dir)
	if err := Check(fsys); err != nil {
		return nil, fmt.Errorf("ui: %s: %w", dir, err)
	}
	return fsys, nil
}

// Check reports whether fsys looks like a build of web/.
func Check(fsys fs.FS) error {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return fmt.Errorf("no index.html — build web/ with `npm run build`: %w", err)
	}
	if !strings.Contains(string(index), `id="root"`) {
		return errors.New("index.html has no mount point")
	}
	// It must reference the bundle, or the page loads and does nothing.
	if !strings.Contains(string(index), "/assets/") {
		return errors.New("index.html references no built asset")
	}
	return nil
}

// Handler serves the interface in fsys.
//
// Everything that is not a known asset returns index.html, so a deep link like
// /teams/design keeps working on reload; the page reads the location itself.
func Handler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := trimLeadingSlash(r.URL.Path)
		if _, err := fs.Stat(fsys, name); err == nil {
			if strings.HasPrefix(name, "assets/") {
				// Vite puts a content hash in these names, so a given URL never
				// changes meaning and can be cached hard. The document below cannot,
				// or a deploy would keep serving the previous bundle.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "ui unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(index)
	})
}

func trimLeadingSlash(p string) string {
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "" {
		return "index.html"
	}
	return p
}
