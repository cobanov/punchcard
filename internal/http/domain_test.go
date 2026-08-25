package http

import (
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/google/uuid"
)

func registerActor(t *testing.T, base, email string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if st, _ := do(t, c, http.MethodPost, base+"/v1/auth/register",
		map[string]string{"email": email, "password": "supersecret123"}, nil); st != http.StatusCreated {
		t.Fatalf("register %s: %d", email, st)
	}
	_, body := do(t, c, http.MethodGet, base+"/v1/me", nil, nil)
	var me struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &me)
	return c, me.ID
}

func must(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

func st(t *testing.T, c *http.Client, method, url string, body any, hdr map[string]string) int {
	t.Helper()
	code, _ := do(t, c, method, url, body, hdr)
	return code
}

// TestAuthorizationMatrix exercises every role x operation x membership case
// plus cross-tenant direct-id attacks. This is the tenant-isolation gate.
func TestAuthorizationMatrix(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "owner@example.com")
	editor, editorID := registerActor(t, base, "editor@example.com")
	viewer, viewerID := registerActor(t, base, "viewer@example.com")
	outsider, outsiderID := registerActor(t, base, "outsider@example.com")

	// Owner creates a shared list and adds the others.
	_, body := do(t, owner, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Team"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &list)
	L := list.ID

	must(t, "add editor", st(t, owner, http.MethodPost, base+"/v1/lists/"+L+"/members",
		map[string]any{"user_id": editorID, "role": "editor"}, csrf), http.StatusNoContent)
	must(t, "add viewer", st(t, owner, http.MethodPost, base+"/v1/lists/"+L+"/members",
		map[string]any{"user_id": viewerID, "role": "viewer"}, csrf), http.StatusNoContent)

	// GET list.
	must(t, "owner GET list", st(t, owner, http.MethodGet, base+"/v1/lists/"+L, nil, nil), 200)
	must(t, "editor GET list", st(t, editor, http.MethodGet, base+"/v1/lists/"+L, nil, nil), 200)
	must(t, "viewer GET list", st(t, viewer, http.MethodGet, base+"/v1/lists/"+L, nil, nil), 200)
	must(t, "outsider GET list (hidden)", st(t, outsider, http.MethodGet, base+"/v1/lists/"+L, nil, nil), 404)

	// Create task: owner/editor allowed, viewer forbidden, outsider hidden.
	_, tbody := do(t, owner, http.MethodPost, base+"/v1/tasks", map[string]any{"list_id": L, "title": "task"}, csrf)
	var task struct {
		ID string `json:"id"`
	}
	unmarshal(t, tbody, &task)
	T := task.ID
	must(t, "editor create task", st(t, editor, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "e"}, csrf), 201)
	must(t, "viewer create task (forbidden)", st(t, viewer, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "v"}, csrf), 403)
	must(t, "outsider create task (hidden)", st(t, outsider, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "o"}, csrf), 404)

	// Read / update / delete task by role.
	must(t, "viewer GET task", st(t, viewer, http.MethodGet, base+"/v1/tasks/"+T, nil, nil), 200)
	must(t, "outsider GET task (hidden)", st(t, outsider, http.MethodGet, base+"/v1/tasks/"+T, nil, nil), 404)
	must(t, "editor PATCH task", st(t, editor, http.MethodPatch, base+"/v1/tasks/"+T,
		map[string]any{"title": "renamed"}, csrf), 200)
	must(t, "viewer PATCH task (forbidden)", st(t, viewer, http.MethodPatch, base+"/v1/tasks/"+T,
		map[string]any{"title": "x"}, csrf), 403)

	// List settings: owner only.
	must(t, "editor rename list (forbidden)", st(t, editor, http.MethodPatch, base+"/v1/lists/"+L,
		map[string]any{"name": "x"}, csrf), 403)
	must(t, "owner rename list", st(t, owner, http.MethodPatch, base+"/v1/lists/"+L,
		map[string]any{"name": "Team2"}, csrf), 200)

	// Members: only owner may add.
	must(t, "editor add member (forbidden)", st(t, editor, http.MethodPost, base+"/v1/lists/"+L+"/members",
		map[string]any{"user_id": outsiderID, "role": "viewer"}, csrf), 403)

	// --- Cross-tenant attacks: outsider hits A's resources by direct id ---
	must(t, "xtenant GET list", st(t, outsider, http.MethodGet, base+"/v1/lists/"+L, nil, nil), 404)
	must(t, "xtenant GET task", st(t, outsider, http.MethodGet, base+"/v1/tasks/"+T, nil, nil), 404)
	must(t, "xtenant PATCH task", st(t, outsider, http.MethodPatch, base+"/v1/tasks/"+T,
		map[string]any{"title": "hacked"}, csrf), 404)
	must(t, "xtenant DELETE task", st(t, outsider, http.MethodDelete, base+"/v1/tasks/"+T, nil, csrf), 404)
	must(t, "xtenant members", st(t, outsider, http.MethodGet, base+"/v1/lists/"+L+"/members", nil, nil), 404)
	must(t, "xtenant delete list", st(t, outsider, http.MethodDelete, base+"/v1/lists/"+L, nil, csrf), 404)

	// Delete task: viewer forbidden, editor allowed, then restore.
	must(t, "viewer DELETE task (forbidden)", st(t, viewer, http.MethodDelete, base+"/v1/tasks/"+T, nil, csrf), 403)
	must(t, "editor DELETE task", st(t, editor, http.MethodDelete, base+"/v1/tasks/"+T, nil, csrf), 204)
	must(t, "editor restore task", st(t, editor, http.MethodPost, base+"/v1/tasks/"+T+"/restore", nil, csrf), 200)

	// Delete list: viewer forbidden, owner allowed (done last).
	must(t, "viewer DELETE list (forbidden)", st(t, viewer, http.MethodDelete, base+"/v1/lists/"+L, nil, csrf), 403)
	must(t, "owner DELETE list", st(t, owner, http.MethodDelete, base+"/v1/lists/"+L, nil, csrf), 204)
}

func TestIdempotencyAndBulk(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	c, _ := registerActor(t, base, "idem@example.com")

	// Inbox is the personal list created at signup.
	_, body := do(t, c, http.MethodGet, base+"/v1/lists", nil, nil)
	var lists struct {
		Lists []struct {
			ID string `json:"id"`
		} `json:"lists"`
	}
	unmarshal(t, body, &lists)
	if len(lists.Lists) != 1 {
		t.Fatalf("expected 1 (Inbox) list at signup, got %d", len(lists.Lists))
	}
	L := lists.Lists[0].ID

	// Idempotent create: same key + body replays the same task.
	key := map[string]string{csrfHeader: csrf[csrfHeader], "Idempotency-Key": "key-1"}
	_, b1 := do(t, c, http.MethodPost, base+"/v1/tasks", map[string]any{"list_id": L, "title": "once"}, key)
	var t1 struct {
		ID string `json:"id"`
	}
	unmarshal(t, b1, &t1)
	st2, b2 := do(t, c, http.MethodPost, base+"/v1/tasks", map[string]any{"list_id": L, "title": "once"}, key)
	must(t, "idempotent replay status", st2, 201)
	var t2 struct {
		ID string `json:"id"`
	}
	unmarshal(t, b2, &t2)
	if t1.ID != t2.ID {
		t.Fatalf("idempotent replay created a new task: %s != %s", t1.ID, t2.ID)
	}

	// Same key, different body -> 409.
	must(t, "idempotency conflict", st(t, c, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "different"}, key), 409)

	// Bulk: one valid create, one create into a non-member list -> partial success.
	otherList := uuid.NewString()
	_, bb := do(t, c, http.MethodPost, base+"/v1/tasks/bulk", map[string]any{
		"operations": []map[string]any{
			{"op": "create", "task": map[string]any{"list_id": L, "title": "ok"}},
			{"op": "create", "task": map[string]any{"list_id": otherList, "title": "nope"}},
		},
	}, csrf)
	var res struct {
		Results []struct {
			Index int  `json:"index"`
			OK    bool `json:"ok"`
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"results"`
	}
	unmarshal(t, bb, &res)
	if len(res.Results) != 2 {
		t.Fatalf("bulk results = %d, want 2", len(res.Results))
	}
	if !res.Results[0].OK {
		t.Fatal("bulk op 0 should succeed")
	}
	if res.Results[1].OK || res.Results[1].Error == nil || res.Results[1].Error.Code != "not_found" {
		t.Fatalf("bulk op 1 should fail with not_found, got %+v", res.Results[1])
	}
}
