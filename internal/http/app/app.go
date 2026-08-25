// Package app serves the web client: one static HTML file with its script and
// styles inline.
//
// It is deliberately the smallest thing that is genuinely usable, and it is
// meant to be replaced. No build step, no dependencies, nothing generated —
// whatever frontend comes next can delete this directory without unpicking
// anything else, and until it does, the product is usable from a browser.
//
// It authenticates with the session cookie like any browser client, which means
// it carries the CSRF token on every mutation. There is no privileged path here
// either: it calls the same public API as the CLI and the menu bar app.
package app

import (
	_ "embed"
	"net/http"
)

//go:embed index.html
var page []byte

// Handler serves the web client.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Short cache: the page is small and shipping a stale client after a
		// deploy is worse than fetching 12 KB again.
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(page)
	}
}
