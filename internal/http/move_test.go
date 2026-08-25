package http

import (
	"net/http"
	"strconv"
	"testing"
)

// TestTaskMoveAcrossLists covers the cross-list move contract: authorization
// on both lists, subtasks traveling with their parent, a moved subtask
// detaching, end-of-list positioning, and the deleted(src)+created(dst) event
// pair the offline delta feed relies on (docs/offline-sync.md).
func TestTaskMoveAcrossLists(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "mv-owner@example.com")
	helper, helperID := registerActor(t, base, "mv-helper@example.com")

	newList := func(name string) string {
		_, body := do(t, owner, http.MethodPost, base+"/v1/lists", map[string]string{"name": name}, csrf)
		var l struct {
			ID string `json:"id"`
		}
		unmarshal(t, body, &l)
		return l.ID
	}
	newTask := func(listID, title string, parent *string) string {
		payload := map[string]any{"list_id": listID, "title": title}
		if parent != nil {
			payload["parent_id"] = *parent
		}
		status, body := do(t, owner, http.MethodPost, base+"/v1/tasks", payload, csrf)
		must(t, "create "+title, status, http.StatusCreated)
		var tk struct {
			ID string `json:"id"`
		}
		unmarshal(t, body, &tk)
		return tk.ID
	}

	A := newList("Source")
	B := newList("Dest")
	head := pullChanges(t, owner, base, "")

	parent := newTask(A, "parent", nil)
	child := newTask(A, "child", &parent)
	newTask(B, "existing-in-B", nil)

	// A task cannot satisfy the one-level nesting invariant by parenting itself.
	must(t, "self-parent", st(t, owner, http.MethodPatch, base+"/v1/tasks/"+parent,
		map[string]any{"parent_id": parent}, csrf), http.StatusUnprocessableEntity)

	// helper is an editor on A only: the destination is invisible → 404.
	must(t, "add helper to A", st(t, owner, http.MethodPost, base+"/v1/lists/"+A+"/members",
		map[string]any{"user_id": helperID, "role": "editor"}, csrf), http.StatusNoContent)
	must(t, "move to unseen list", st(t, helper, http.MethodPatch, base+"/v1/tasks/"+parent,
		map[string]any{"list_id": B}, csrf), http.StatusNotFound)

	// As a viewer on B the move is forbidden; as an editor it succeeds.
	must(t, "add helper to B as viewer", st(t, owner, http.MethodPost, base+"/v1/lists/"+B+"/members",
		map[string]any{"user_id": helperID, "role": "viewer"}, csrf), http.StatusNoContent)
	must(t, "move as viewer on dest", st(t, helper, http.MethodPatch, base+"/v1/tasks/"+parent,
		map[string]any{"list_id": B}, csrf), http.StatusForbidden)

	// Combining a move with a parent change is rejected.
	must(t, "move + parent combo", st(t, owner, http.MethodPatch, base+"/v1/tasks/"+parent,
		map[string]any{"list_id": B, "parent_id": child}, csrf), 422)

	// Owner moves the parent: 200, child follows, task lands at the end of B.
	moveHead := pullChanges(t, owner, base, "")
	status, body := do(t, owner, http.MethodPatch, base+"/v1/tasks/"+parent, map[string]any{"list_id": B}, csrf)
	must(t, "owner moves parent", status, http.StatusOK)
	var moved struct {
		ListID   string  `json:"list_id"`
		ParentID *string `json:"parent_id"`
		Position string  `json:"position"`
	}
	unmarshal(t, body, &moved)
	if moved.ListID != B || moved.ParentID != nil {
		t.Fatalf("moved task: list=%s parent=%v, want list B, no parent", moved.ListID, moved.ParentID)
	}

	var listB struct {
		Tasks []struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			ListID   string  `json:"list_id"`
			ParentID *string `json:"parent_id"`
			Position string  `json:"position"`
		} `json:"tasks"`
	}
	_, body = do(t, owner, http.MethodGet, base+"/v1/tasks?list_id="+B+"&sort=position&limit=100", nil, nil)
	unmarshal(t, body, &listB)
	if len(listB.Tasks) != 3 {
		t.Fatalf("list B: got %d tasks, want 3 (existing + parent + child)", len(listB.Tasks))
	}
	byTitle := map[string]int{}
	for i, tk := range listB.Tasks {
		byTitle[tk.Title] = i
		if tk.Title == "child" && (tk.ParentID == nil || *tk.ParentID != parent) {
			t.Fatalf("child lost its parent on move: %v", tk.ParentID)
		}
	}
	// End-of-list position: the moved parent sorts after B's pre-existing task.
	if byTitle["existing-in-B"] >= byTitle["parent"] {
		t.Fatalf("moved task did not land at the end: order %v", byTitle)
	}
	_, body = do(t, owner, http.MethodGet, base+"/v1/tasks?list_id="+A+"&limit=100", nil, nil)
	var listA struct {
		Tasks []struct{} `json:"tasks"`
	}
	unmarshal(t, body, &listA)
	if len(listA.Tasks) != 0 {
		t.Fatalf("list A still has %d tasks after the move", len(listA.Tasks))
	}

	// Delta feed: the move reads as deleted-on-A + created-on-B, for parent
	// and child alike (grace window is 0 in tests).
	page := pullChanges(t, owner, base, "?since="+strconv.FormatInt(moveHead.Next, 10))
	var delA, creB int
	for _, c := range page.Changes {
		var env struct {
			ListID *string `json:"list_id"`
		}
		unmarshal(t, c.Data, &env)
		if c.Type == "task.deleted" && env.ListID != nil && *env.ListID == A {
			delA++
		}
		if c.Type == "task.created" && env.ListID != nil && *env.ListID == B {
			creB++
		}
	}
	if delA != 2 || creB != 2 {
		t.Fatalf("move events: %d deleted-on-A, %d created-on-B, want 2/2", delA, creB)
	}

	// Moving a subtask by itself detaches it.
	p2 := newTask(B, "p2", nil)
	c2 := newTask(B, "c2", &p2)
	status, body = do(t, owner, http.MethodPatch, base+"/v1/tasks/"+c2, map[string]any{"list_id": A}, csrf)
	must(t, "move subtask alone", status, http.StatusOK)
	unmarshal(t, body, &moved)
	if moved.ListID != A || moved.ParentID != nil {
		t.Fatalf("moved subtask: list=%s parent=%v, want list A and detached", moved.ListID, moved.ParentID)
	}

	// The events pair for the earlier head still replays coherently.
	full := pullChanges(t, owner, base, "?since="+strconv.FormatInt(head.Next, 10)+"&limit=500")
	if len(full.Changes) == 0 || full.Reset {
		t.Fatalf("full replay: %d changes reset=%v", len(full.Changes), full.Reset)
	}
}

// TestNestingDepthIsCappedAtBothEnds guards the one-level nesting invariant from
// the side that used to be unguarded. UpdateTask only ever checked the
// prospective parent (that it is not itself a subtask), never the task being
// reparented — so giving a parent to a task that already had children built a
// three-deep chain. The grandchild is invisible to every UI, and a cross-list
// move of that chain stranded it: MoveSubtasksToList follows exactly one level,
// leaving the grandchild behind with a parent_id pointing into the other list.
func TestNestingDepthIsCappedAtBothEnds(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "nest-owner@example.com")

	_, body := do(t, owner, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Nest"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &list)

	newTask := func(title string, parent *string) string {
		payload := map[string]any{"list_id": list.ID, "title": title}
		if parent != nil {
			payload["parent_id"] = *parent
		}
		status, b := do(t, owner, http.MethodPost, base+"/v1/tasks", payload, csrf)
		must(t, "create "+title, status, http.StatusCreated)
		var tk struct {
			ID string `json:"id"`
		}
		unmarshal(t, b, &tk)
		return tk.ID
	}

	root := newTask("root", nil)
	newTask("leaf", &root) // root now has a child
	other := newTask("other", nil)

	// Known end: a task whose prospective parent is already a subtask.
	must(t, "parent is itself a subtask", st(t, owner, http.MethodPatch, base+"/v1/tasks/"+other,
		map[string]any{"parent_id": newTask("leaf2", &root)}, csrf), http.StatusUnprocessableEntity)

	// The end this test exists for: root has children, so it may not become one.
	must(t, "task with children becomes a subtask", st(t, owner, http.MethodPatch, base+"/v1/tasks/"+root,
		map[string]any{"parent_id": other}, csrf), http.StatusUnprocessableEntity)

	// The rejection left root untouched, not half-applied.
	_, body = do(t, owner, http.MethodGet, base+"/v1/tasks/"+root, nil, nil)
	var got struct {
		ParentID *string `json:"parent_id"`
	}
	unmarshal(t, body, &got)
	if got.ParentID != nil {
		t.Fatalf("root parent_id = %v after a rejected reparent, want nil", *got.ParentID)
	}

	// The legitimate case still works: a childless task can become a subtask.
	must(t, "childless task becomes a subtask", st(t, owner, http.MethodPatch, base+"/v1/tasks/"+other,
		map[string]any{"parent_id": root}, csrf), http.StatusOK)
}
