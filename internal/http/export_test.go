package http

import (
	"bytes"
	"net/http"
	"testing"
)

// TestGDPRExportStream verifies the streamed export is well-formed JSON and
// contains the account's lists and tasks (ATD-26: streamed, not materialized).
func TestGDPRExportStream(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	c, _ := registerActor(t, base, "export@example.com")

	_, lb := do(t, c, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Work"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, lb, &list)

	for _, title := range []string{"a", "b", "c"} {
		if st, _ := do(t, c, http.MethodPost, base+"/v1/tasks",
			map[string]any{"list_id": list.ID, "title": title}, csrf); st != http.StatusCreated {
			t.Fatalf("create task %s: %d", title, st)
		}
	}

	st, body := do(t, c, http.MethodGet, base+"/v1/me/export", nil, nil)
	if st != http.StatusOK {
		t.Fatalf("export = %d, want 200", st)
	}

	var exp struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
		Lists []struct {
			List struct {
				ID string `json:"id"`
			} `json:"list"`
			Role  string `json:"role"`
			Tasks []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"tasks"`
		} `json:"lists"`
		Tokens []any `json:"tokens"`
	}
	unmarshal(t, body, &exp) // fails if the streamed JSON is malformed

	if exp.User.Email != "export@example.com" {
		t.Fatalf("export user email = %q", exp.User.Email)
	}
	total := 0
	found := false
	for _, l := range exp.Lists {
		total += len(l.Tasks)
		if l.List.ID == list.ID {
			found = true
			if len(l.Tasks) != 3 {
				t.Fatalf("Work list task count = %d, want 3", len(l.Tasks))
			}
		}
	}
	if !found {
		t.Fatal("created list missing from export")
	}
	if total < 3 {
		t.Fatalf("total exported tasks = %d, want >= 3", total)
	}
}

// TestGDPRExportIsSessionOnly locks the export to the session plane. It reaches
// every list the account can see — soft-deleted tasks included — and lists the
// metadata of the account's other PATs, so it is account-plane data like
// DELETE /v1/me and token management. Before this, the handler checked nothing
// past authentication: the weakest possible token (read scope, narrowed to one
// list) could dump the entire account.
func TestGDPRExportIsSessionOnly(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	session, _ := registerActor(t, base, "export-scope@example.com")

	_, body := do(t, session, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Secret"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &list)
	must(t, "create task", st(t, session, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": list.ID, "title": "confidential"}, csrf), http.StatusCreated)

	// The narrowest token the API can mint: read-only, one list.
	status, body := do(t, session, http.MethodPost, base+"/v1/tokens", map[string]any{
		"name": "narrow", "scope": "read", "scoped_list_ids": []string{list.ID},
	}, csrf)
	must(t, "create narrow token", status, http.StatusCreated)
	var token struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, body, &token)

	bearer := &http.Client{}
	headers := map[string]string{"Authorization": "Bearer " + token.Secret}

	status, body = do(t, bearer, http.MethodGet, base+"/v1/me/export", nil, headers)
	must(t, "export via PAT", status, http.StatusForbidden)
	// The refusal must arrive as a problem document, not as a 200 truncated
	// mid-stream — authorization is settled before the headers go out.
	if bytes.Contains(body, []byte("confidential")) {
		t.Fatalf("rejected export leaked task data: %s", body)
	}

	// A read_write, unscoped token is still a token: same answer.
	status, body = do(t, session, http.MethodPost, base+"/v1/tokens",
		map[string]any{"name": "wide", "scope": "read_write"}, csrf)
	must(t, "create wide token", status, http.StatusCreated)
	unmarshal(t, body, &token)
	must(t, "export via read_write PAT", st(t, bearer, http.MethodGet, base+"/v1/me/export", nil,
		map[string]string{"Authorization": "Bearer " + token.Secret}), http.StatusForbidden)

	// The session itself is unaffected.
	must(t, "export via session", st(t, session, http.MethodGet, base+"/v1/me/export", nil, nil),
		http.StatusOK)
}
