package http

import (
	"bytes"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// idempotencyMiddleware implements idempotency for authenticated POST
// requests carrying an Idempotency-Key: the same key + same body replays the
// stored response; the same key + a different body returns 409. Keys are scoped
// per user with a 24h TTL (purged by a River job in Phase 3).
func (d Deps) idempotencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		p := PrincipalFrom(r.Context())
		if r.Method != http.MethodPost || key == "" || p == nil || d.Store == nil || !strings.HasPrefix(r.URL.Path, "/v1") {
			next.ServeHTTP(w, r)
			return
		}
		// Bound the key so it can't overflow the storage column (which would 500).
		if len(key) > 255 {
			writeProblem(w, http.StatusBadRequest, "validation_failed", "Idempotency-Key must be at most 255 characters")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "bad_request", "could not read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		sum := sha256.Sum256([]byte(r.Method + "\n" + r.URL.Path + "\n" + string(body)))
		hash := sum[:]

		ctx := r.Context()
		existing, err := d.Store.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{UserID: p.UserID, Key: key})
		switch {
		case err == nil:
			if !bytes.Equal(existing.RequestHash, hash) {
				writeProblem(w, http.StatusConflict, "idempotency_conflict",
					"Idempotency-Key was reused with a different request body")
				return
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(int(existing.ResponseStatus))
			_, _ = w.Write(existing.ResponseBody)
			return
		case !repo.IsNotFound(err):
			// Fail open on storage errors rather than blocking the request.
			d.Logger.ErrorContext(ctx, "idempotency lookup failed", "error", err.Error())
			next.ServeHTTP(w, r)
			return
		}

		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		for k, vs := range rec.Header() {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())

		// Only persist deterministic outcomes; let 5xx be retried.
		if rec.Code < 500 {
			status := int32(200)
			if rec.Code >= 0 && rec.Code <= 599 {
				status = int32(rec.Code)
			}
			if _, err := d.Store.CreateIdempotencyKey(ctx, db.CreateIdempotencyKeyParams{
				UserID: p.UserID, Key: key, RequestHash: hash,
				ResponseStatus: status, ResponseBody: rec.Body.Bytes(),
			}); err != nil {
				d.Logger.ErrorContext(ctx, "idempotency store failed", "error", err.Error())
			}
		}
	})
}
