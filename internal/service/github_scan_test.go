package service

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/config"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/testutil"
)

// --- a fake GitHub, service-side -----------------------------------------
//
// internal/github has its own fake for testing the scanner's branch walking.
// This one is deliberately separate and much simpler: these tests are about
// what the SERVICE does with what the scanner returns — the state machine, the
// backoff, the attribution — so the API only has to be plausible, not faithful.

type ghCommit struct {
	sha    string
	at     time.Time
	branch string
}

type fakeGH struct {
	*httptest.Server
	mu      sync.Mutex
	commits map[string][]ghCommit // repo full name -> commits
	stale   map[string]bool       // repos whose last push is long past
	paths   []string
	status  int
	calls   int
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	f := &fakeGH{commits: map[string][]ghCommit{}, stale: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// addStaleRepo registers a repository the account owns but has not pushed to in
// a long time.
func (f *fakeGH) addStaleRepo(repo string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stale[repo] = true
}

// touched reports whether any request mentioned the repository.
func (f *fakeGH) touched(repo string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.paths {
		if strings.Contains(p, repo) {
			return true
		}
	}
	return false
}

func (f *fakeGH) addCommit(repo, branch, sha string, when time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits[repo] = append(f.commits[repo], ghCommit{sha: sha, at: when, branch: branch})
}

func (f *fakeGH) failWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeGH) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.paths = append(f.paths, r.URL.Path)
	status := f.status
	f.mu.Unlock()
	if status != 0 {
		if status == http.StatusForbidden {
			w.Header().Set("X-RateLimit-Remaining", "0")
		}
		w.WriteHeader(status)
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	w.Header().Set("Content-Type", "application/json")

	if r.URL.Path == "/user" {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "cobanov", "id": 1})
		return
	}
	// Repository discovery: everything with a commit, plus anything explicitly
	// registered as stale, ordered newest push first like GitHub does.
	if r.URL.Path == "/user/repos" {
		f.mu.Lock()
		fresh := make([]string, 0, len(f.commits))
		for name := range f.commits {
			fresh = append(fresh, name)
		}
		staleNames := make([]string, 0, len(f.stale))
		for name := range f.stale {
			staleNames = append(staleNames, name)
		}
		f.mu.Unlock()
		sort.Strings(fresh)
		sort.Strings(staleNames)

		out := []map[string]any{}
		for _, name := range fresh {
			out = append(out, map[string]any{
				"full_name": name, "default_branch": "main",
				"pushed_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			})
		}
		for _, name := range staleNames {
			out = append(out, map[string]any{
				"full_name": name, "default_branch": "main",
				"pushed_at": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
			})
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	if len(parts) < 3 || parts[0] != "repos" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	full := parts[1] + "/" + parts[2]

	switch {
	case len(parts) == 3:
		// Always claim a recent push so nothing is skipped for staleness; the
		// skip path has its own coverage in internal/github.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"full_name": full, "default_branch": "main",
			"pushed_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		})
	case parts[3] == "branches":
		f.mu.Lock()
		seen := map[string]bool{"main": true}
		for _, c := range f.commits[full] {
			seen[c.branch] = true
		}
		f.mu.Unlock()
		out := make([]map[string]string, 0, len(seen))
		for name := range seen {
			out = append(out, map[string]string{"name": name})
		}
		_ = json.NewEncoder(w).Encode(out)
	case parts[3] == "commits":
		q := r.URL.Query()
		branch := q.Get("sha")
		since, _ := time.Parse(time.RFC3339, q.Get("since"))
		until, _ := time.Parse(time.RFC3339, q.Get("until"))

		f.mu.Lock()
		all := append([]ghCommit(nil), f.commits[full]...)
		f.mu.Unlock()

		out := []map[string]any{}
		for _, c := range all {
			if branch != "" && c.branch != branch {
				continue
			}
			if c.at.Before(since) || c.at.After(until) {
				continue
			}
			out = append(out, map[string]any{
				"sha": c.sha, "html_url": "https://example.test/" + c.sha,
				"commit": map[string]any{
					"message":   "fixture " + c.sha,
					"committer": map[string]any{"date": c.at.UTC().Format(time.RFC3339)},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// newGitHubEnv is a test environment whose Domain has a token cipher and points
// at the fake API.
func newGitHubEnv(t *testing.T) (*testEnv, *fakeGH) {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	cfg := &config.Config{
		Env: config.EnvDevelopment, PublicBaseURL: "http://localhost:8080",
		EmailProvider: "dev", MaxPATsPerUser: 25,
		MaxProjectsPerUser: 500, MaxWebhooksPerUser: 10,
	}
	cipher, err := NewGitHubCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	d := NewDomain(store, audit.NewLogger(store, logger), noopSender{}, nil, cipher, logger, cfg)
	gh := newFakeGH(t)
	d.UseGitHubAPI(gh.URL, gh.Client())
	return &testEnv{d: d, pool: pool, ctx: t.Context()}, gh
}

// connectedUser registers an account with a GitHub connection and a project
// linked to cobanov/x.
func (e *testEnv) connectedUser(t *testing.T) (*auth.Principal, db.Project) {
	t.Helper()
	p := e.newUser(t)
	if err := e.d.ConnectGitHub(e.ctx, p, "cobanov", "ghp_secret_value", GitHubScope); err != nil {
		t.Fatalf("connect github: %v", err)
	}
	proj := e.mustProject(t, p, "p")
	if _, err := e.d.LinkRepo(e.ctx, p, proj.ID, "cobanov/x"); err != nil {
		t.Fatalf("link repo: %v", err)
	}
	return p, proj
}

func (e *testEnv) commitSHAs(t *testing.T, p *auth.Principal, sessionID uuid.UUID) []string {
	t.Helper()
	rows, err := e.d.CommitsForSession(e.ctx, p, sessionID)
	if err != nil {
		t.Fatalf("commits for session: %v", err)
	}
	return shas(rows)
}

// --- token storage --------------------------------------------------------

// A token in the clear is a token in every backup.
func TestTokenIsNotStoredInPlaintext(t *testing.T) {
	e, _ := newGitHubEnv(t)
	p, _ := e.connectedUser(t)

	var raw []byte
	if err := e.pool.QueryRow(e.ctx,
		`SELECT access_token_enc FROM github_connections WHERE user_id = $1`, p.UserID).Scan(&raw); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if strings.Contains(string(raw), "ghp_secret_value") {
		t.Fatal("the access token is readable in the database")
	}
}

func TestStatusNeverLeaksTheToken(t *testing.T) {
	e, _ := newGitHubEnv(t)
	p, _ := e.connectedUser(t)

	st, err := e.d.GitHubStatus(e.ctx, p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	body, _ := json.Marshal(st)
	if strings.Contains(string(body), "ghp_secret_value") {
		t.Fatalf("status leaked the token: %s", body)
	}
	if !st.Connected || st.Login != "cobanov" {
		t.Fatalf("status = %+v", st)
	}
}

// --- attribution ----------------------------------------------------------

func TestScanAttachesCommitsToTheCoveringSession(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("11:15"))

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 || got[0] != "aaaaaaa" {
		t.Fatalf("commits = %v, want [aaaaaaa]", got)
	}
}

// A commit on a feature branch must land too — this is the trap the whole
// scanner is shaped around, asserted here at the service level as well because
// it is the behaviour a user would notice.
func TestScanAttachesFeatureBranchCommits(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addCommit("cobanov/x", "feature/refactor", "bbbbbbb", at("11:15"))

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 {
		t.Fatalf("commits = %v, want the feature-branch commit", got)
	}
}

// The second trap: a commit written during a session but pushed hours later is
// invisible to the first scan, and must be picked up by the periodic re-scan.
func TestRescanAttachesLatePushedCommits(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 0 {
		t.Fatalf("nothing was pushed yet, but got %v", got)
	}

	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("11:15")) // pushed in the evening

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 {
		t.Fatalf("the late push should have been attached, got %v", got)
	}
}

// Re-scanning is safe to repeat: the upsert and the attach are both idempotent.
func TestScanIsIdempotent(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("11:15"))

	for i := 0; i < 3; i++ {
		if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 {
		t.Fatalf("commits = %v, want exactly one after three scans", got)
	}
}

// An account with no GitHub connection is not a failure; it is a user who has
// not connected GitHub.
func TestScanWithoutAConnectionIsANoOp(t *testing.T) {
	e, _ := newGitHubEnv(t)
	p := e.newUser(t)
	proj := e.mustProject(t, p, "p")
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("scan without a connection should be a no-op, got %v", err)
	}
}

// --- the state machine ----------------------------------------------------

func TestPendingScanRunsAndMarksTheSessionOK(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("11:15"))

	if err := e.d.RunPendingScans(e.ctx, time.Now(), 50); err != nil {
		t.Fatalf("run pending: %v", err)
	}
	reloaded, _ := e.d.GetSession(e.ctx, p, ws.ID)
	if reloaded.SyncState != "ok" {
		t.Fatalf("sync_state = %q, want ok", reloaded.SyncState)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 {
		t.Fatalf("commits = %v", got)
	}
}

// A transient failure backs off and, after the last step, stops retrying rather
// than hammering GitHub forever.
func TestFailedScanBacksOffAndEventuallyErrors(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.failWith(http.StatusInternalServerError)

	now := time.Now()
	for i := 0; i <= len(backoff); i++ {
		if err := e.d.RunPendingScans(e.ctx, now, 50); err != nil {
			t.Fatalf("run pending %d: %v", i, err)
		}
		now = now.Add(24 * time.Hour) // every scheduled retry is due
	}
	reloaded, _ := e.d.GetSession(e.ctx, p, ws.ID)
	if reloaded.SyncState != "error" {
		t.Fatalf("sync_state = %q, want error after exhausting the backoff", reloaded.SyncState)
	}
	if reloaded.SyncError == nil || *reloaded.SyncError == "" {
		t.Fatal("a failed scan must record why")
	}
}

// A revoked token parks the queue and surfaces on the connection. Retrying it on
// a schedule would spend quota on a credential GitHub already rejected, and
// worse, the user would never learn why their commits stopped arriving.
func TestRevokedTokenParksTheQueueAndIsReported(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.failWith(http.StatusUnauthorized)

	if err := e.d.RunPendingScans(e.ctx, time.Now(), 50); err != nil {
		t.Fatalf("run pending: %v", err)
	}
	reloaded, _ := e.d.GetSession(e.ctx, p, ws.ID)
	if reloaded.SyncState != "skipped" {
		t.Fatalf("sync_state = %q, want skipped", reloaded.SyncState)
	}
	st, err := e.d.GitHubStatus(e.ctx, p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.LastError == "" {
		t.Fatal("the user must be able to see why scanning stopped")
	}
}

// A rate-limit pause is transient and must not be mistaken for a dead token.
func TestRateLimitedScanRetriesRatherThanParking(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.failWith(http.StatusForbidden) // the fake sets X-RateLimit-Remaining: 0

	if err := e.d.RunPendingScans(e.ctx, time.Now(), 50); err != nil {
		t.Fatalf("run pending: %v", err)
	}
	reloaded, _ := e.d.GetSession(e.ctx, p, ws.ID)
	if reloaded.SyncState != "pending" {
		t.Fatalf("sync_state = %q, want pending (a quota pause is not a dead token)", reloaded.SyncState)
	}
}

func TestRequeueRecentSessionsPicksUpFinishedWork(t *testing.T) {
	e, _ := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, time.Now().Add(-3*time.Hour), time.Now().Add(-2*time.Hour))
	if err := e.d.store.SetSessionSyncOK(e.ctx, ws.ID); err != nil {
		t.Fatalf("mark ok: %v", err)
	}

	n, err := e.d.RequeueRecentSessions(e.ctx, time.Now())
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if n != 1 {
		t.Fatalf("requeued %d sessions, want 1", n)
	}
}

// --- unmatched commits ----------------------------------------------------

func TestCommitOutsideAnySessionStaysUnmatched(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addCommit("cobanov/x", "main", "inside0", at("11:00"))
	gh.addCommit("cobanov/x", "main", "outside", at("14:00"))

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, at("08:00"), at("18:00")); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 || got[0] != "inside0" {
		t.Fatalf("session commits = %v", got)
	}

	clusters, err := e.d.UnmatchedClusters(e.ctx, p, at("08:00"), at("18:00"))
	if err != nil {
		t.Fatalf("unmatched: %v", err)
	}
	if len(clusters) != 1 || len(clusters[0].Commits) != 1 || clusters[0].Commits[0].Sha != "outside" {
		t.Fatalf("clusters = %+v", clusters)
	}
}

func TestClustersSplitOnGapsLongerThanThirtyMinutes(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, _ := e.connectedUser(t)
	for _, c := range []struct {
		sha, clock string
	}{
		{"aaaaaaa", "14:00"}, {"bbbbbbb", "14:20"},
		{"ccccccc", "15:30"}, {"ddddddd", "15:40"},
	} {
		gh.addCommit("cobanov/x", "main", c.sha, at(c.clock))
	}
	if _, err := e.d.ScanWindow(e.ctx, p.UserID, at("08:00"), at("18:00")); err != nil {
		t.Fatalf("scan: %v", err)
	}

	clusters, err := e.d.UnmatchedClusters(e.ctx, p, at("08:00"), at("18:00"))
	if err != nil {
		t.Fatalf("unmatched: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("want two clusters split on the 70-minute gap, got %d", len(clusters))
	}
	if len(clusters[0].Commits) != 2 || len(clusters[1].Commits) != 2 {
		t.Fatalf("cluster sizes = %d, %d", len(clusters[0].Commits), len(clusters[1].Commits))
	}
}

// One repository linked to exactly one project is the only case where the
// project can be guessed without risking billing the wrong client.
func TestClusterSuggestsTheProjectWhenUnambiguous(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("14:00"))
	if _, err := e.d.ScanWindow(e.ctx, p.UserID, at("08:00"), at("18:00")); err != nil {
		t.Fatalf("scan: %v", err)
	}

	clusters, _ := e.d.UnmatchedClusters(e.ctx, p, at("08:00"), at("18:00"))
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d", len(clusters))
	}
	if clusters[0].SuggestedProjectID == nil || *clusters[0].SuggestedProjectID != proj.ID {
		t.Fatalf("suggested project = %v, want %s", clusters[0].SuggestedProjectID, proj.ID)
	}
	if clusters[0].SuggestedNote == "" {
		t.Fatal("the suggestion should carry a note built from the commit messages")
	}
}

// With the repository on two projects there is no safe guess, so none is made.
func TestClusterMakesNoSuggestionWhenTheRepoServesTwoProjects(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, _ := e.connectedUser(t)
	other := e.mustProject(t, p, "other")
	if _, err := e.d.LinkRepo(e.ctx, p, other.ID, "cobanov/x"); err != nil {
		t.Fatalf("link second project: %v", err)
	}
	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("14:00"))
	if _, err := e.d.ScanWindow(e.ctx, p.UserID, at("08:00"), at("18:00")); err != nil {
		t.Fatalf("scan: %v", err)
	}

	clusters, _ := e.d.UnmatchedClusters(e.ctx, p, at("08:00"), at("18:00"))
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d", len(clusters))
	}
	if clusters[0].SuggestedProjectID != nil {
		t.Fatal("a repository on two projects must not be guessed")
	}
}

// The recovery path end to end: a forgotten timer becomes a real record.
func TestSessionFromClusterAttachesItsCommits(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	gh.addCommit("cobanov/x", "main", "aaaaaaa", at("14:00"))
	gh.addCommit("cobanov/x", "main", "bbbbbbb", at("14:20"))
	if _, err := e.d.ScanWindow(e.ctx, p.UserID, at("08:00"), at("18:00")); err != nil {
		t.Fatalf("scan: %v", err)
	}
	clusters, _ := e.d.UnmatchedClusters(e.ctx, p, at("08:00"), at("18:00"))
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d", len(clusters))
	}

	ws, err := e.d.SessionFromCluster(e.ctx, p, ClusterToSessionInput{
		ProjectID: proj.ID, From: clusters[0].From, To: clusters[0].To, Note: "kurtarıldı",
	})
	if err != nil {
		t.Fatalf("session from cluster: %v", err)
	}
	if ws.Source != "auto" {
		t.Fatalf("source = %q, want auto so a reconstructed record is not mistaken for a timed one", ws.Source)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 2 {
		t.Fatalf("commits = %v, want both", got)
	}
	left, _ := e.d.UnmatchedClusters(e.ctx, p, at("08:00"), at("18:00"))
	if len(left) != 0 {
		t.Fatalf("the cluster should be matched now, still see %+v", left)
	}
}

// A suggested window that overlaps a real session would break the one-session-
// per-instant rule.
func TestSessionFromClusterRejectsAnOverlappingWindow(t *testing.T) {
	e, _ := newGitHubEnv(t)
	p, proj := e.connectedUser(t)
	e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))

	_, err := e.d.SessionFromCluster(e.ctx, p, ClusterToSessionInput{
		ProjectID: proj.ID, From: at("11:00"), To: at("13:00"), Note: "çakışan",
	})
	var de *Error
	if !asError(err, &de) || de.Status != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}

// Linking a repository to a project must be OPTIONAL.
//
// Which session a commit lands in is decided by TIME, not by which project a
// repository belongs to. Scanning only linked repositories turned an optional
// refinement into a setup step: a user who connected GitHub and started a timer
// got nothing, with no error to explain it. The scanner discovers repositories
// the account pushed to during the window instead.
func TestCommitsAttachWithoutAnyLinkedRepo(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p := e.newUser(t)
	if err := e.d.ConnectGitHub(e.ctx, p, "cobanov", "ghp_secret_value", GitHubScope); err != nil {
		t.Fatalf("connect github: %v", err)
	}
	proj := e.mustProject(t, p, "p") // deliberately NO LinkRepo
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addCommit("cobanov/somewhere", "main", "aaaaaaa", at("11:15"))

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := e.commitSHAs(t, p, ws.ID); len(got) != 1 || got[0] != "aaaaaaa" {
		t.Fatalf("commits = %v — a commit must attach with no repository linked", got)
	}
}

// A repository nobody has pushed to since the window began cannot hold commits
// inside it, so it is never fetched. This is what keeps discovery cheap.
func TestDiscoveryIgnoresRepositoriesNotPushedSinceTheWindow(t *testing.T) {
	e, gh := newGitHubEnv(t)
	p := e.newUser(t)
	if err := e.d.ConnectGitHub(e.ctx, p, "cobanov", "ghp_secret_value", GitHubScope); err != nil {
		t.Fatalf("connect github: %v", err)
	}
	proj := e.mustProject(t, p, "p")
	ws := e.seedSession(t, p, proj.ID, at("10:00"), at("12:00"))
	gh.addStaleRepo("cobanov/abandoned")

	if _, err := e.d.ScanWindow(e.ctx, p.UserID, ws.StartedAt, *ws.EndedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gh.touched("cobanov/abandoned") {
		t.Fatal("a repository with no pushes since the window began should never be fetched")
	}
}
