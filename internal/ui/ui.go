// Package ui serves the browser interface.
//
// It is plain HTML, CSS and JavaScript embedded into the binary — no build step,
// no toolchain, nothing to install on the server. That is the same reason the
// rest of this system is a static binary: the deployment target is one machine
// an administrator maintains by hand, and every tool it needs at build time is a
// thing that can be missing or the wrong version at the moment it is needed.
//
// The interface is the REQUIRED path (nas-design.md §2): the people it is for do
// not mount SMB shares, so anything they cannot do here they cannot do at all.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var content embed.FS

// Handler serves the interface.
//
// Everything that is not a known asset returns index.html, so a deep link like
// /teams/design keeps working on reload; the page reads the location itself.
func Handler() http.Handler {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic("ui: " + err.Error())
	}
	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); err == nil {
			// The assets are content-addressed by nothing, so they must not be
			// cached across a deploy; the documents are small.
			w.Header().Set("Cache-Control", "no-cache")
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
