// Package ui serves the browser interface.
//
// The interface is written in TypeScript and React under web/ and built by Vite.
// What is embedded here is the BUILD OUTPUT, which is committed: `go build`
// alone still produces a server with nothing to install beside it, so Node is a
// dependency of changing the interface and never of running or deploying it.
//
// scripts/build-ui.sh rebuilds it, and TestEmbeddedBuildIsPresent below fails if
// the output is missing. Whether it is CURRENT is checked in CI by rebuilding
// and diffing, because nothing inside a Go test can know what the sources would
// have produced.
//
// The interface is the REQUIRED path (nas-design.md §2): the people it is for do
// not mount SMB shares, so anything they cannot do here they cannot do at all.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var content embed.FS

// Handler serves the interface.
//
// Everything that is not a known asset returns index.html, so a deep link like
// /teams/design keeps working on reload; the page reads the location itself.
func Handler() http.Handler {
	sub, err := fs.Sub(content, "dist")
	if err != nil {
		panic("ui: " + err.Error())
	}
	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := trimLeadingSlash(r.URL.Path)
		if _, err := fs.Stat(sub, name); err == nil {
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
		index, err := fs.ReadFile(sub, "index.html")
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
