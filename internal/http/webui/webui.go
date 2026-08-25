// Package webui serves the built web application.
//
// The bundle is embedded in the binary, so a self-hosted punchcard stays one
// file with no assets to deploy alongside it. That convenience has a cost worth
// naming: the server serves the EMBEDDED dist, not whatever is in web/dist. A
// frontend change that was built but not rebuilt-and-restarted here is a change
// nobody will see, and the symptom is a page that looks stale for no reason.
// `make web` is what closes that gap; the Makefile says so too.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var assets embed.FS

// Handler serves the app, with every unmatched path falling back to index.html
// so client-side routing works on a hard refresh.
func Handler() http.Handler {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		// The embed pattern not matching is a build-time fact, not a runtime
		// condition — fail at wiring rather than serving a 500 later.
		panic("webui: " + err.Error())
	}
	files := http.FileServer(http.FS(dist))
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		panic("webui: dist/index.html is missing — run make web")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || path == "app" {
			serveIndex(w, index)
			return
		}
		if _, err := fs.Stat(dist, path); err != nil {
			serveIndex(w, index)
			return
		}
		// Vite fingerprints asset filenames, so they can be cached hard.
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Never cached: it names the fingerprinted assets, so a stale index is a
	// stale app.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(index)
}
