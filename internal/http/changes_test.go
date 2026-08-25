package http

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type changesPage struct {
	Changes []struct {
		Seq  int64           `json:"seq"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"changes"`
	Next    int64 `json:"next"`
	HasMore bool  `json:"has_more"`
	Reset   bool  `json:"reset"`
}

func pullChanges(t *testing.T, c *http.Client, base, query string) changesPage {
	t.Helper()
	status, body := do(t, c, http.MethodGet, base+"/v1/changes"+query, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/changes%s: %d (%s)", query, status, body)
	}
	var page changesPage
	unmarshal(t, body, &page)
	return page
}

// TestChangesFeed exercises the offline-sync delta protocol end to end:
// head-only bootstrap, seq-ordered delivery, pagination, membership scoping at
// read time (including revocation), and the future-cursor reset.
func TestChangesFeed(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "chg-owner@example.com")
	viewer, viewerID := registerActor(t, base, "chg-viewer@example.com")
	outsider, _ := registerActor(t, base, "chg-outsider@example.com")

	// Head-only mode: no since → just the current cursor.
	head := pullChanges(t, owner, base, "")
	if len(head.Changes) != 0 || head.Reset {
		t.Fatalf("head-only pull: got %d changes (reset=%v), want none", len(head.Changes), head.Reset)
	}
	since := strconv.FormatInt(head.Next, 10)

	// Mutations write the outbox; grace window is 0 in testConfig.
	_, body := do(t, owner, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Sync"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &list)
	L := list.ID
	must(t, "task 1", st(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "t1"}, csrf), http.StatusCreated)
	must(t, "task 2", st(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "t2"}, csrf), http.StatusCreated)

	page := pullChanges(t, owner, base, "?since="+since)
	if len(page.Changes) != 3 {
		t.Fatalf("delta pull: got %d changes, want 3", len(page.Changes))
	}
	wantTypes := []string{"list.created", "task.created", "task.created"}
	for i, c := range page.Changes {
		if c.Type != wantTypes[i] {
			t.Fatalf("change %d: type %s, want %s", i, c.Type, wantTypes[i])
		}
		if i > 0 && c.Seq <= page.Changes[i-1].Seq {
			t.Fatalf("changes not strictly seq-ordered: %d then %d", page.Changes[i-1].Seq, c.Seq)
		}
	}
	if page.Next != page.Changes[2].Seq || page.Reset || page.HasMore {
		t.Fatalf("delta pull: next=%d hasMore=%v reset=%v, want next=last seq", page.Next, page.HasMore, page.Reset)
	}
	// The envelope embeds the full resource snapshot.
	var env struct {
		Resource struct {
			Title string `json:"title"`
		} `json:"resource"`
	}
	unmarshal(t, page.Changes[1].Data, &env)
	if env.Resource.Title != "t1" {
		t.Fatalf("envelope resource title = %q, want t1", env.Resource.Title)
	}

	// Pagination.
	p1 := pullChanges(t, owner, base, "?since="+since+"&limit=1")
	if len(p1.Changes) != 1 || !p1.HasMore {
		t.Fatalf("limit=1: got %d changes hasMore=%v", len(p1.Changes), p1.HasMore)
	}

	// Cursor at head → empty, cursor unchanged.
	pHead := pullChanges(t, owner, base, "?since="+strconv.FormatInt(page.Next, 10))
	if len(pHead.Changes) != 0 || pHead.Next != page.Next {
		t.Fatalf("pull at head: %d changes next=%d, want 0 changes next=%d", len(pHead.Changes), pHead.Next, page.Next)
	}

	// Scoping: a non-member sees none of the list's events.
	pOut := pullChanges(t, outsider, base, "?since="+since)
	if len(pOut.Changes) != 0 {
		t.Fatalf("outsider pull: got %d changes, want 0", len(pOut.Changes))
	}

	// Membership grants event history; revocation removes it (read-time scoping).
	must(t, "add viewer", st(t, owner, http.MethodPost, base+"/v1/lists/"+L+"/members",
		map[string]any{"user_id": viewerID, "role": "viewer"}, csrf), http.StatusNoContent)
	pV := pullChanges(t, viewer, base, "?since="+since)
	if len(pV.Changes) < 4 { // list.created + 2× task.created + member.added
		t.Fatalf("member pull: got %d changes, want ≥4", len(pV.Changes))
	}
	must(t, "remove viewer", st(t, owner, http.MethodDelete, base+"/v1/lists/"+L+"/members/"+viewerID, nil, csrf), http.StatusNoContent)
	pV2 := pullChanges(t, viewer, base, "?since="+since)
	if len(pV2.Changes) != 0 {
		// Documented consequence: the removed member cannot see member.removed
		// either — clients reconcile via GET /v1/lists (docs/offline-sync.md §6).
		t.Fatalf("revoked member pull: got %d changes, want 0", len(pV2.Changes))
	}

	// A cursor from the future (database restore) forces a resync.
	pR := pullChanges(t, owner, base, "?since=99999999")
	if !pR.Reset {
		t.Fatal("future cursor: want reset=true")
	}

	// Validation: negative since rejected by the schema.
	must(t, "negative since", st(t, owner, http.MethodGet, base+"/v1/changes?since=-1", nil, nil), 422)
}

// TestClientProvidedIDs covers offline clients pre-generating UUIDv7 ids on
// create for tasks (single + bulk) and lists: acceptance, version validation,
// and id_conflict on replay/collision — including cross-tenant.
func TestClientProvidedIDs(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "cid-owner@example.com")
	outsider, _ := registerActor(t, base, "cid-outsider@example.com")

	newV7 := func() string {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		return id.String()
	}

	// List create with a client id.
	listID := newV7()
	status, body := do(t, owner, http.MethodPost, base+"/v1/lists",
		map[string]string{"id": listID, "name": "Client"}, csrf)
	must(t, "create list with id", status, http.StatusCreated)
	var created struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &created)
	if created.ID != listID {
		t.Fatalf("list id = %s, want client id %s", created.ID, listID)
	}
	must(t, "duplicate list id", st(t, owner, http.MethodPost, base+"/v1/lists",
		map[string]string{"id": listID, "name": "Again"}, csrf), http.StatusConflict)

	// Task create with a client id.
	taskID := newV7()
	status, body = do(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"id": taskID, "list_id": listID, "title": "x"}, csrf)
	must(t, "create task with id", status, http.StatusCreated)
	unmarshal(t, body, &created)
	if created.ID != taskID {
		t.Fatalf("task id = %s, want client id %s", created.ID, taskID)
	}

	// Replaying the same id conflicts with a stable machine-readable code.
	status, body = do(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"id": taskID, "list_id": listID, "title": "x"}, csrf)
	must(t, "duplicate task id", status, http.StatusConflict)
	var problem struct {
		Code string `json:"code"`
	}
	unmarshal(t, body, &problem)
	if problem.Code != "id_conflict" {
		t.Fatalf("conflict code = %q, want id_conflict", problem.Code)
	}

	// Non-v7 ids are rejected: PKs must stay time-sortable.
	must(t, "v4 id rejected", st(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"id": uuid.NewString(), "list_id": listID, "title": "x"}, csrf), 422)

	// Bulk create honors the payload id; replay surfaces a per-op id_conflict.
	bulkID := newV7()
	bulk := func() (int, []byte) {
		return do(t, owner, http.MethodPost, base+"/v1/tasks/bulk", map[string]any{
			"operations": []map[string]any{
				{"op": "create", "task": map[string]any{"id": bulkID, "list_id": listID, "title": "b"}},
			},
		}, csrf)
	}
	status, body = bulk()
	must(t, "bulk create with id", status, http.StatusOK)
	var bulkRes struct {
		Results []struct {
			OK   bool `json:"ok"`
			Task *struct {
				ID string `json:"id"`
			} `json:"task"`
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"results"`
	}
	unmarshal(t, body, &bulkRes)
	if !bulkRes.Results[0].OK || bulkRes.Results[0].Task.ID != bulkID {
		t.Fatalf("bulk create: ok=%v id=%v, want ok with client id", bulkRes.Results[0].OK, bulkRes.Results[0].Task)
	}
	status, body = bulk()
	must(t, "bulk replay", status, http.StatusOK)
	unmarshal(t, body, &bulkRes)
	if bulkRes.Results[0].OK || bulkRes.Results[0].Error == nil || bulkRes.Results[0].Error.Code != "id_conflict" {
		t.Fatalf("bulk replay: got %+v, want per-op id_conflict", bulkRes.Results[0])
	}

	// Cross-tenant: ids are globally unique; a foreign create with a taken id
	// conflicts without touching the original (membership still gates access).
	_, obody := do(t, outsider, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Mine"}, csrf)
	var olist struct {
		ID string `json:"id"`
	}
	unmarshal(t, obody, &olist)
	must(t, "xtenant id collision", st(t, outsider, http.MethodPost, base+"/v1/tasks",
		map[string]any{"id": taskID, "list_id": olist.ID, "title": "steal"}, csrf), http.StatusConflict)
	must(t, "original task intact", st(t, owner, http.MethodGet, base+"/v1/tasks/"+taskID, nil, nil), 200)
	must(t, "outsider cannot read it", st(t, outsider, http.MethodGet, base+"/v1/tasks/"+taskID, nil, nil), 404)
}

// TestSSEResumeFromSeq verifies the stream replays missed events when resumed
// with a numeric (seq) Last-Event-ID, and emits seq ids on every event.
func TestSSEResumeFromSeq(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "sse-seq@example.com")

	head := pullChanges(t, owner, base, "")
	_, body := do(t, owner, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Stream"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &list)
	must(t, "create task", st(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": list.ID, "title": "missed"}, csrf), http.StatusCreated)

	// Resume from the pre-mutation head: both events must be replayed with
	// numeric ids.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", strconv.FormatInt(head.Next, 10))
	resp, err := owner.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var got []string
	var lastID int64 = -1
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && len(got) < 2 {
		line := sc.Text()
		if strings.HasPrefix(line, "id: ") {
			id, perr := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if perr != nil {
				t.Fatalf("stream id is not a seq: %q", line)
			}
			if id <= lastID {
				t.Fatalf("stream ids not increasing: %d then %d", lastID, id)
			}
			lastID = id
		}
		if strings.HasPrefix(line, "event: ") {
			got = append(got, strings.TrimPrefix(line, "event: "))
		}
	}
	if len(got) < 2 || got[0] != "list.created" || got[1] != "task.created" {
		t.Fatalf("resumed stream events = %v, want [list.created task.created]", got)
	}
}
