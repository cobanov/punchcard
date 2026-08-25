// Package legal serves punchcard's privacy policy and terms of use.
//
// These are static, self-contained HTML documents rather than routes in the
// SPA, and that is deliberate. App Store Connect requires a privacy policy URL
// and App Review opens it directly; so do link previews, crawlers and anyone
// reading on a slow connection. A page that needs a 340 KB React bundle to say
// "we do not sell your data" is a page that can fail to say it.
//
// They carry their own styling for the same reason. The palette matches
// theme.css by hand — the design-system rule against a second `:root` palette
// is about styles.css and Tailwind disagreeing inside one app, and these
// documents share no cascade with either.
package legal

import (
	"embed"
	"net/http"
)

//go:embed *.html
var pages embed.FS

// Handler serves one embedded document with long-lived caching. The documents
// change on the order of once a year; App Review and crawlers both benefit from
// them being cheap.
func Handler(name string) http.HandlerFunc {
	body, err := pages.ReadFile(name)
	if err != nil {
		// Impossible unless the embed pattern stops matching, which is a build-
		// time fact — fail loudly at wiring time rather than serving a 500 later.
		panic("legal: " + err.Error())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
	}
}
