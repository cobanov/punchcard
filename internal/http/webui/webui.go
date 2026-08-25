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

// Handler serves the application. Every unmatched path under it falls back to
// index.html so client-side routing survives a hard refresh.
func Handler() http.Handler {
	dist := sub()
	files := http.FileServer(http.FS(dist))
	index := mustRead(dist, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || path == "app" {
			servePage(w, index)
			return
		}
		if _, err := fs.Stat(dist, path); err != nil {
			servePage(w, index)
			return
		}
		// Vite fingerprints asset filenames, so they can be cached forever.
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// Landing serves the marketing page at the root.
//
// It is a separate entry from the app deliberately: it is what a stranger sees
// first, and it must paint without waiting for the application bundle. It ships
// its own stylesheet and a script small enough to be inlined in one packet.
func Landing() http.HandlerFunc {
	page := mustRead(sub(), "landing.html")
	return func(w http.ResponseWriter, r *http.Request) {
		servePage(w, page)
	}
}

func sub() fs.FS {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		// The embed pattern not matching is a build-time fact, not a runtime
		// condition — fail at wiring rather than serving a 500 later.
		panic("webui: " + err.Error())
	}
	return dist
}

func mustRead(dist fs.FS, name string) []byte {
	body, err := fs.ReadFile(dist, name)
	if err != nil {
		panic("webui: dist/" + name + " is missing — run make web")
	}
	return body
}

func servePage(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Never cached: it names the fingerprinted assets, so a stale page is a
	// stale build.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
