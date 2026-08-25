// Package landing serves the one page punchcard has: the thing a person sees
// when they type the bare hostname.
//
// It exists because of what happened without it. v1 dropped the SPA that used
// to answer "/", nothing replaced it, and chi's default handler answered every
// visitor with the bare words "404 page not found" — a healthy service that
// read as broken to anyone who opened it, including its author.
//
// The page is static, self-contained and carries its own styling. There is no
// build step and no bundle to keep in sync with the binary, which is the whole
// reason v1 could drop the frontend toolchain in the first place.
package landing

import (
	_ "embed"
	"net/http"
)

//go:embed index.html
var page []byte

// Handler serves the landing page.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(page)
	}
}
