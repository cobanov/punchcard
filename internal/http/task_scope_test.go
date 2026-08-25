package http

import (
	"net/http"
	"net/url"
	"testing"
)

func TestListScopedTokenIsolationAndTaskPagination(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	session, ownerID := registerActor(t, base, "scoped-pagination@example.com")
	_, memberID := registerActor(t, base, "scoped-pagination-member@example.com")

	_, body := do(t, session, http.MethodGet, base+"/v1/lists", nil, nil)
	var initial struct {
		Lists []struct {
			ID string `json:"id"`
		} `json:"lists"`
	}
	unmarshal(t, body, &initial)
	if len(initial.Lists) != 1 {
		t.Fatalf("initial lists = %d, want Inbox only", len(initial.Lists))
	}
	excludedListID := initial.Lists[0].ID

	status, _ := do(t, session, http.MethodPost, base+"/v1/lists/"+excludedListID+"/members",
		map[string]any{"user_id": memberID, "role": "viewer"}, csrf)
	must(t, "add excluded-list member", status, http.StatusNoContent)

	_, body = do(t, session, http.MethodPost, base+"/v1/lists",
		map[string]string{"name": "Scoped"}, csrf)
	var scopedList struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &scopedList)

	createTask := func(listID, title string) {
		t.Helper()
		status, _ := do(t, session, http.MethodPost, base+"/v1/tasks",
			map[string]any{"list_id": listID, "title": title}, csrf)
		must(t, "create "+title, status, http.StatusCreated)
	}

	// Scoped tasks are deliberately older than excluded tasks. The old
	// implementation applied LIMIT before token filtering, returning an empty
	// first page even though the token could access both scoped tasks.
	createTask(scopedList.ID, "scoped-older")
	createTask(scopedList.ID, "scoped-newer")
	createTask(excludedListID, "excluded-older")
	createTask(excludedListID, "excluded-newer")

	status, body = do(t, session, http.MethodPost, base+"/v1/tokens", map[string]any{
		"name":            "one-list",
		"scope":           "read_write",
		"scoped_list_ids": []string{scopedList.ID},
	}, csrf)
	must(t, "create scoped token", status, http.StatusCreated)
	var token struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, body, &token)
	headers := map[string]string{"Authorization": "Bearer " + token.Secret}
	bearer := &http.Client{}

	status, _ = do(t, bearer, http.MethodDelete,
		base+"/v1/lists/"+excludedListID+"/members/"+memberID, nil, headers)
	must(t, "out-of-scope member removal", status, http.StatusNotFound)

	// Confirm the failed removal did not mutate the excluded list.
	status, body = do(t, session, http.MethodGet,
		base+"/v1/lists/"+excludedListID+"/members", nil, nil)
	must(t, "list excluded-list members", status, http.StatusOK)
	var members struct {
		Members []struct {
			UserID string `json:"user_id"`
		} `json:"members"`
	}
	unmarshal(t, body, &members)
	gotMembers := map[string]bool{}
	for _, member := range members.Members {
		gotMembers[member.UserID] = true
	}
	if !gotMembers[ownerID] || !gotMembers[memberID] {
		t.Fatalf("excluded-list members = %v, want owner and viewer", gotMembers)
	}

	type tasksPage struct {
		Tasks []struct {
			Title  string `json:"title"`
			ListID string `json:"list_id"`
		} `json:"tasks"`
		NextCursor string `json:"next_cursor"`
	}

	status, body = do(t, bearer, http.MethodGet,
		base+"/v1/tasks?sort=created_at&limit=1", nil, headers)
	must(t, "scoped first page", status, http.StatusOK)
	var first tasksPage
	unmarshal(t, body, &first)
	if len(first.Tasks) != 1 || first.Tasks[0].Title != "scoped-newer" ||
		first.Tasks[0].ListID != scopedList.ID {
		t.Fatalf("first page = %+v, want scoped-newer", first.Tasks)
	}
	if first.NextCursor == "" {
		t.Fatal("first page has no next cursor")
	}

	status, body = do(t, bearer, http.MethodGet,
		base+"/v1/tasks?sort=created_at&limit=1&cursor="+url.QueryEscape(first.NextCursor),
		nil, headers)
	must(t, "scoped second page", status, http.StatusOK)
	var second tasksPage
	unmarshal(t, body, &second)
	if len(second.Tasks) != 1 || second.Tasks[0].Title != "scoped-older" ||
		second.Tasks[0].ListID != scopedList.ID {
		t.Fatalf("second page = %+v, want scoped-older", second.Tasks)
	}
	if second.NextCursor != "" {
		t.Fatalf("second page next cursor = %q, want empty", second.NextCursor)
	}
}
