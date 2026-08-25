package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCommit is a commit on a branch of the fake server.
type fakeCommit struct {
	SHA    string
	At     time.Time
	Author string
}

// fakeRepo is one repository the fake server serves.
type fakeRepo struct {
	FullName string
	PushedAt time.Time
	Default  string
	Branches map[string][]fakeCommit
}

// fakeGitHub is a stand-in for api.github.com.
//
// The scanner is tested against this rather than the real API for two reasons:
// a real call spends quota and needs a token, and — more importantly — the two
// traps this file exists to pin down (the default-branch listing and the
// unpushed commit) are both about what the API does NOT return, which is far
// easier to state as a fixture than to arrange upstream.
type fakeGitHub struct {
	*httptest.Server
	mu     sync.Mutex
	repos  map[string]*fakeRepo
	calls  []string
	status int // when non-zero, every request answers with it
}

func newFakeGitHub(t *testing.T, repos ...fakeRepo) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{repos: map[string]*fakeRepo{}}
	for i := range repos {
		r := repos[i]
		if r.Default == "" {
			r.Default = "main"
		}
		f.repos[r.FullName] = &r
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
}

// callCount returns how many requests whose path contains substr were made.
func (f *fakeGitHub) callCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func (f *fakeGitHub) failWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.record(r.URL.Path + "?" + r.URL.RawQuery)

	f.mu.Lock()
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
	// /repos/{owner}/{repo}[/branches|/commits]
	if len(parts) < 3 || parts[0] != "repos" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	full := parts[1] + "/" + parts[2]
	f.mu.Lock()
	repo, ok := f.repos[full]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch {
	case len(parts) == 3:
		writeJSON(w, map[string]any{
			"full_name": repo.FullName, "default_branch": repo.Default,
			"pushed_at": repo.PushedAt.UTC().Format(time.RFC3339), "private": false,
		})
	case parts[3] == "branches":
		names := make([]map[string]string, 0, len(repo.Branches))
		for name := range repo.Branches {
			names = append(names, map[string]string{"name": name})
		}
		writeJSON(w, names)
	case parts[3] == "commits":
		q := r.URL.Query()
		// This is the fake's whole point: with no sha it answers for the
		// DEFAULT BRANCH ONLY, exactly like GitHub.
		branch := q.Get("sha")
		if branch == "" {
			branch = repo.Default
		}
		since, _ := time.Parse(time.RFC3339, q.Get("since"))
		until, _ := time.Parse(time.RFC3339, q.Get("until"))
		author := q.Get("author")

		out := []map[string]any{}
		for _, c := range repo.Branches[branch] {
			if !since.IsZero() && c.At.Before(since) {
				continue
			}
			if !until.IsZero() && c.At.After(until) {
				continue
			}
			if author != "" && c.Author != "" && c.Author != author {
				continue
			}
			out = append(out, map[string]any{
				"sha": c.SHA, "html_url": "https://github.com/" + full + "/commit/" + c.SHA,
				"commit": map[string]any{
					"message":   "fixture " + c.SHA,
					"committer": map[string]any{"date": c.At.UTC().Format(time.RFC3339)},
					"author":    map[string]any{"date": c.At.UTC().Format(time.RFC3339)},
				},
			})
		}
		writeJSON(w, out)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func at(clock string) time.Time {
	ts, err := time.Parse(time.RFC3339, "2026-03-01T"+clock+":00Z")
	if err != nil {
		panic(err)
	}
	return ts
}

func shas(commits []Commit) []string {
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.SHA)
	}
	return out
}

// THE trap. GitHub's commit listing walks the default branch and nothing else,
// so a developer on a feature branch gets an empty answer — and empty is
// indistinguishable from "no work happened", which means the failure is silent.
func TestScanFindsCommitsOnNonDefaultBranches(t *testing.T) {
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x",
		PushedAt: at("12:00"),
		Default:  "main",
		Branches: map[string][]fakeCommit{
			"main":             {{SHA: "aaaaaaa", At: at("09:00"), Author: "cobanov"}},
			"feature/refactor": {{SHA: "bbbbbbb", At: at("10:30"), Author: "cobanov"}},
		},
	})
	c := New(f.Client(), f.URL, "token")

	got, err := ScanRepo(context.Background(), c, "cobanov/x", "cobanov", at("08:00"), at("12:00"), nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got.Commits) != 2 {
		t.Fatalf("commits = %v, want both branches represented", shas(got.Commits))
	}
}

// A commit reachable from several branches is still one commit.
func TestScanDeduplicatesCommitsSeenOnTwoBranches(t *testing.T) {
	shared := fakeCommit{SHA: "aaaaaaa", At: at("09:00"), Author: "cobanov"}
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x", PushedAt: at("12:00"),
		Branches: map[string][]fakeCommit{
			"main":    {shared},
			"release": {shared},
		},
	})
	c := New(f.Client(), f.URL, "token")

	got, err := ScanRepo(context.Background(), c, "cobanov/x", "cobanov", at("08:00"), at("12:00"), nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got.Commits) != 1 {
		t.Fatalf("commits = %v, want one", shas(got.Commits))
	}
}

// A repository nobody pushed to during the window is settled in one request.
func TestScanSkipsRepoPushedBeforeTheWindow(t *testing.T) {
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x",
		PushedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		Branches: map[string][]fakeCommit{
			"main": {{SHA: "aaaaaaa", At: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Author: "cobanov"}},
		},
	})
	c := New(f.Client(), f.URL, "token")

	got, err := ScanRepo(context.Background(), c, "cobanov/x", "cobanov", at("08:00"), at("12:00"), nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !got.Skipped || len(got.Commits) != 0 {
		t.Fatalf("want a skip, got %+v", got)
	}
	if n := f.callCount("/branches"); n != 0 {
		t.Fatalf("branches were listed %d times for a repository that could not match", n)
	}
	if n := f.callCount("/commits"); n != 0 {
		t.Fatalf("commits were listed %d times for a repository that could not match", n)
	}
}

// Someone else's commits in your repository are not your work.
func TestScanFiltersByAuthor(t *testing.T) {
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x", PushedAt: at("12:00"),
		Branches: map[string][]fakeCommit{
			"main": {
				{SHA: "aaaaaaa", At: at("09:00"), Author: "cobanov"},
				{SHA: "ccccccc", At: at("09:30"), Author: "someone-else"},
			},
		},
	})
	c := New(f.Client(), f.URL, "token")

	got, _ := ScanRepo(context.Background(), c, "cobanov/x", "cobanov", at("08:00"), at("12:00"), nil)
	if len(got.Commits) != 1 || got.Commits[0].SHA != "aaaaaaa" {
		t.Fatalf("commits = %v, want only cobanov's", shas(got.Commits))
	}
}

// The window is honoured, so a scan of one session cannot pick up another's work.
func TestScanHonoursTheWindow(t *testing.T) {
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x", PushedAt: at("18:00"),
		Branches: map[string][]fakeCommit{
			"main": {
				{SHA: "inside0", At: at("10:30"), Author: "cobanov"},
				{SHA: "outside", At: at("14:00"), Author: "cobanov"},
			},
		},
	})
	c := New(f.Client(), f.URL, "token")

	got, _ := ScanRepo(context.Background(), c, "cobanov/x", "cobanov", at("10:00"), at("12:00"), nil)
	if len(got.Commits) != 1 || got.Commits[0].SHA != "inside0" {
		t.Fatalf("commits = %v, want only the one inside the window", shas(got.Commits))
	}
}

// A cached branch list keeps the per-branch enumeration off the wire.
func TestScanUsesCachedBranches(t *testing.T) {
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x", PushedAt: at("12:00"),
		Branches: map[string][]fakeCommit{"main": {{SHA: "aaaaaaa", At: at("09:00"), Author: "cobanov"}}},
	})
	c := New(f.Client(), f.URL, "token")

	if _, err := ScanRepo(context.Background(), c, "cobanov/x", "cobanov",
		at("08:00"), at("12:00"), []string{"main"}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n := f.callCount("/branches"); n != 0 {
		t.Fatalf("branches fetched %d times despite a cache", n)
	}
}

// A revoked token and a rate-limit pause must not look the same: one needs the
// user to reconnect, the other just needs waiting.
func TestUnauthorizedAndRateLimitedAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrRateLimited}, // the fake sets Remaining: 0
		{http.StatusTooManyRequests, ErrRateLimited},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newFakeGitHub(t, fakeRepo{FullName: "cobanov/x", PushedAt: at("12:00")})
			f.failWith(tc.status)
			c := New(f.Client(), f.URL, "token")

			_, err := ScanRepo(context.Background(), c, "cobanov/x", "cobanov", at("08:00"), at("12:00"), nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d gave %v, want %v", tc.status, err, tc.want)
			}
		})
	}
}

// A branch that answers 404 (deleted between the listing and the fetch) must not
// fail the whole scan.
func TestMissingBranchDoesNotFailTheScan(t *testing.T) {
	f := newFakeGitHub(t, fakeRepo{
		FullName: "cobanov/x", PushedAt: at("12:00"),
		Branches: map[string][]fakeCommit{"main": {{SHA: "aaaaaaa", At: at("09:00"), Author: "cobanov"}}},
	})
	c := New(f.Client(), f.URL, "token")

	got, err := ScanRepo(context.Background(), c, "cobanov/x", "cobanov",
		at("08:00"), at("12:00"), []string{"main", "branch-that-vanished"})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got.Commits) != 1 {
		t.Fatalf("commits = %v, want the surviving branch's commit", shas(got.Commits))
	}
}
