package http

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

// activityItem is the subset of GET /v1/activity's wire response these tests
// assert on. Deliberately a distinct type from the handler's own
// activityItemDTO (added in a later step of this task) rather than that type
// itself: this file has to compile — and fail on a 404, not a build error —
// before activity_handler.go exists at all.
type activityItem struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

// activityPageResult is one decoded page of GET /v1/activity.
type activityPageResult struct {
	Items []activityItem
	Next  string
}

// activityPage fetches one page of GET /v1/activity as actor. query is the
// raw query string including its leading "?", or "" for none.
func (e *apiEnv) activityPage(t *testing.T, actor *http.Client, query string) activityPageResult {
	t.Helper()
	status, body := do(t, actor, http.MethodGet, e.base+"/v1/activity"+query, nil, nil)
	must(t, "get activity", status, http.StatusOK)
	var page struct {
		Items []activityItem `json:"items"`
		Next  string         `json:"next"`
	}
	unmarshal(t, body, &page)
	return activityPageResult{Items: page.Items, Next: page.Next}
}

// activity is activityPage minus the cursor, for the tests that only care
// which rows came back.
func (e *apiEnv) activity(t *testing.T, actor *http.Client, query string) []activityItem {
	t.Helper()
	return e.activityPage(t, actor, query).Items
}

// containsSubject reports whether any item in a page carries the given
// subject.
func containsSubject(items []activityItem, subject string) bool {
	for _, it := range items {
		if it.Subject == subject {
			return true
		}
	}
	return false
}

// createList creates a list as actor and returns its id.
func (e *apiEnv) createList(t *testing.T, actor *http.Client, name string) string {
	t.Helper()
	status, body := do(t, actor, http.MethodPost, e.base+"/v1/lists", map[string]string{"name": name}, testCSRF())
	must(t, "create list "+name, status, http.StatusCreated)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &list)
	return list.ID
}

// createTaskAs creates a task on listID as actor and asserts it succeeds.
// Every call site is expected to have write access to listID at the moment
// it's called — a test that needs to exercise a blocked write (a removed
// member's own list, say) does so with a direct do() call instead, so a
// silent authorization failure here can't be mistaken for the read-side
// behavior a test is actually checking.
func (e *apiEnv) createTaskAs(t *testing.T, actor *http.Client, listID, title string) {
	t.Helper()
	status, _ := do(t, actor, http.MethodPost, e.base+"/v1/tasks",
		map[string]any{"list_id": listID, "title": title}, testCSRF())
	must(t, "create task "+title, status, http.StatusCreated)
}

// shareListWith creates a fresh list owned by the primary session and adds
// actor to it as an editor — enough role to create tasks, which every test
// using this needs. A fresh list, never env.listID, keeps a test's
// assertions clear of whatever newAPIEnv's own setup wrote.
func (e *apiEnv) shareListWith(t *testing.T, actor *http.Client) string {
	t.Helper()
	listID := e.createList(t, e.session, "Shared")
	status, body := do(t, actor, http.MethodGet, e.base+"/v1/me", nil, nil)
	must(t, "resolve actor id", status, http.StatusOK)
	var me struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &me)
	must(t, "add member", st(t, e.session, http.MethodPost, e.base+"/v1/lists/"+listID+"/members",
		map[string]any{"user_id": me.ID, "role": "editor"}, testCSRF()), http.StatusNoContent)
	return listID
}

// removeMember removes userID from listID, acting as the list's owning
// session.
func (e *apiEnv) removeMember(t *testing.T, listID string, userID uuid.UUID) {
	t.Helper()
	must(t, "remove member", st(t, e.session, http.MethodDelete,
		e.base+"/v1/lists/"+listID+"/members/"+userID.String(), nil, testCSRF()), http.StatusNoContent)
}

// mintPAT creates a PAT scoped to listIDs (unrestricted when empty) with the
// given scope, and returns a client that authenticates with it.
func (e *apiEnv) mintPAT(t *testing.T, scope string, listIDs []string) *http.Client {
	t.Helper()
	status, body := do(t, e.session, http.MethodPost, e.base+"/v1/tokens",
		map[string]any{"name": "scoped-" + scope, "scope": scope, "scoped_list_ids": listIDs}, testCSRF())
	must(t, "mint pat", status, http.StatusCreated)
	var tok struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, body, &tok)
	return &http.Client{Transport: bearerRT{pat: tok.Secret, base: http.DefaultTransport}}
}

// itoa saves a dozen strconv.Itoa qualifications in the pagination test's loop.
func itoa(i int) string { return strconv.Itoa(i) }

// A member of a shared list sees what happened on it, because a log that
// cannot answer "who finished this?" has lost half its reason to exist.
func TestActivityShowsOtherMembersOnSharedLists(t *testing.T) {
	env := newAPIEnv(t)
	shared := env.shareListWith(t, env.other)
	env.createTaskAs(t, env.other, shared, "Send the invoice")

	items := env.activity(t, env.session, "")
	if !containsSubject(items, "Send the invoice") {
		t.Fatal("a co-member's action is missing from the log")
	}
}

// Your own history is yours; a list's ongoing history is not. Read as the
// removed member, not the owner: env.session was never removed and remains
// a member of shared throughout, so it would see both rows regardless of
// which half of the authorization clause's OR is doing the work — reading
// this way, neither assertion would fail if the "my own actions" branch
// (own_user_id) were deleted outright, and the removal in this test's name
// would go unexercised. Only the removed member's own view can tell the two
// branches apart: with a broken own-actions branch, "theirs, before removal"
// disappears from their own view even though the list branch still can't
// supply it (they're no longer a member), so it must fail specifically
// through that row's absence.
func TestRemovedMemberKeepsOwnRowsAndLosesTheList(t *testing.T) {
	env := newAPIEnv(t)
	shared := env.shareListWith(t, env.other)
	env.createTaskAs(t, env.session, shared, "mine, before removal")
	env.createTaskAs(t, env.other, shared, "theirs, before removal")
	env.removeMember(t, shared, env.otherUserID)

	items := env.activity(t, env.other, "")
	if !containsSubject(items, "theirs, before removal") {
		t.Fatal("a removed member lost their own history, not just the list's")
	}
	if containsSubject(items, "mine, before removal") {
		t.Fatal("a removed member still reads the list's ongoing history")
	}
}

// THE trap. Leaving the "my own actions" branch in place for a list-scoped
// token lets it read the user's activity on lists outside its scope straight
// through the OR — the same class of defect as checking only one end of a
// relationship, closed in 0.4.6.
func TestListScopedPATCannotReachOutsideItsScope(t *testing.T) {
	env := newAPIEnv(t)
	inScope := env.listID
	outOfScope := env.createList(t, env.session, "Private")
	env.createTaskAs(t, env.session, inScope, "in scope")
	env.createTaskAs(t, env.session, outOfScope, "out of scope")

	scoped := env.mintPAT(t, "read", []string{inScope})

	items := env.activity(t, scoped, "")
	if !containsSubject(items, "in scope") {
		t.Fatal("a scoped token cannot read its own list")
	}
	if containsSubject(items, "out of scope") {
		t.Fatal("SCOPE ESCAPE: the token read a list outside its scope")
	}

	// mine is a display filter layered on top of the authorization clause, not
	// a second route to it — it must narrow the same already-scoped set, never
	// widen past it. Both rows were created by the same account the token
	// belongs to, so a query that let mine=true bypass the token's list scope
	// (e.g. a future refactor folding the display clause into the
	// authorization clause) would leak "out of scope" here even though the
	// unfiltered request above already caught the coarser version of the leak.
	mine := env.activity(t, scoped, "?mine=true")
	if !containsSubject(mine, "in scope") {
		t.Fatal("mine=true hid the token's own in-scope row")
	}
	if containsSubject(mine, "out of scope") {
		t.Fatal("SCOPE ESCAPE: mine=true read a list outside its scope")
	}
}

// mine is a display filter, and must narrow within the authorization scope
// rather than widen past it.
func TestMineFilterNarrowsWithinScope(t *testing.T) {
	env := newAPIEnv(t)
	shared := env.shareListWith(t, env.other)
	env.createTaskAs(t, env.session, shared, "mine")
	env.createTaskAs(t, env.other, shared, "theirs")

	items := env.activity(t, env.session, "?mine=true")
	if !containsSubject(items, "mine") {
		t.Fatal("mine=true hid my own row")
	}
	if containsSubject(items, "theirs") {
		t.Fatal("mine=true still returned another member's row")
	}
}

func TestOriginFilter(t *testing.T) {
	env := newAPIEnv(t)
	env.createTaskAs(t, env.session, env.listID, "by hand")
	env.createTaskAs(t, env.pat, env.listID, "by a program")

	items := env.activity(t, env.session, "?origin=api")
	if containsSubject(items, "by hand") {
		t.Fatal("origin=api returned a user row")
	}
	if !containsSubject(items, "by a program") {
		t.Fatal("origin=api dropped the api row")
	}
}

func TestActivityPaginatesWithoutRepeatingOrSkipping(t *testing.T) {
	env := newAPIEnv(t)

	// newAPIEnv already wrote at least one row for this session before this
	// test starts (creating env.listID itself is a logged action, via
	// Domain.CreateList's events.Write) — measured, not assumed, so this test
	// doesn't silently drift out of sync with whatever newAPIEnv's setup does
	// in the future. "Exactly two full pages, and the second one also the
	// last" needs a real boundary to land on: that's the one case a next
	// cursor gated on "page came back full" cannot tell apart from "there's
	// another page behind this" — see ListActivity's hasMore, computed from
	// the pre-truncation row count instead.
	const perPage = 10
	baseline := len(env.activityPage(t, env.session, "?limit=200").Items)
	need := 2*perPage - baseline
	if need < 1 {
		t.Fatalf("newAPIEnv already produced %d activity rows for this session; test assumptions no longer hold", baseline)
	}
	for i := 0; i < need; i++ {
		env.createTaskAs(t, env.session, env.listID, "task "+itoa(i))
	}

	first := env.activityPage(t, env.session, "?limit="+itoa(perPage))
	if len(first.Items) != perPage {
		t.Fatalf("first page: got %d, want %d", len(first.Items), perPage)
	}
	if first.Next == "" {
		t.Fatal("first page is full with more rows behind it; next must not be empty")
	}
	second := env.activityPage(t, env.session, "?limit="+itoa(perPage)+"&before="+first.Next)
	seen := map[string]bool{}
	for _, it := range append(first.Items, second.Items...) {
		if seen[it.ID] {
			t.Fatalf("id %s returned on both pages", it.ID)
		}
		seen[it.ID] = true
	}
	if len(seen) != 2*perPage {
		t.Fatalf("got %d distinct rows across two pages, want %d", len(seen), 2*perPage)
	}
	if second.Next != "" {
		t.Fatal("second page is full but also the last one; next must be empty, not a cursor to an empty page")
	}
}

func TestActivityRequiresAuth(t *testing.T) {
	env := newAPIEnv(t)
	status, _ := do(t, &http.Client{}, http.MethodGet, env.base+"/v1/activity", nil, nil)
	must(t, "anonymous activity", status, http.StatusUnauthorized)
}

// The 422 is deliberate, not incidental: ParseActivityCursor's doc comment
// says an unparseable cursor must error rather than silently restart a
// paginating client at the newest page forever. Pin it so a future "be
// forgiving about bad input" pass doesn't quietly turn this into a 200.
func TestActivityBeforeMustBeAValidCursor(t *testing.T) {
	env := newAPIEnv(t)
	status, _ := do(t, env.session, http.MethodGet, env.base+"/v1/activity?before=not-a-cursor", nil, nil)
	must(t, "unparseable cursor", status, http.StatusUnprocessableEntity)
}
