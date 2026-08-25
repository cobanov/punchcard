package github

import (
	"context"
	"time"
)

// BranchCacheTTL is how long a repository's branch list is reused before it is
// fetched again.
const BranchCacheTTL = time.Hour

// RepoScan is one repository's result.
type RepoScan struct {
	FullName string
	Commits  []Commit
	Branches []string // the branch list actually used, for caching
	Skipped  bool     // true when pushed_at ruled the repository out
}

// ScanRepo returns the author's commits in a repository between since and until.
//
// # The default-branch trap
//
// `GET /repos/{owner}/{repo}/commits` with no `sha` parameter lists commits on
// the DEFAULT BRANCH ONLY. For anyone working on a feature branch — which is
// most people, most weeks — that call returns an empty list.
//
// An empty list is the worst possible failure here, because it is
// indistinguishable from "no work happened". The feature does not error, it does
// not warn, it just quietly reports nothing and the user concludes the product
// does not work. So the scan enumerates branches and asks per branch, deduping
// by SHA (a commit reachable from three branches is still one commit).
//
// The cost is one extra request per repository plus one per branch, against a
// 5000/hour budget. A repository with twenty branches costs twenty-one requests
// per scan, and cachedBranches keeps even that off the wire for an hour.
//
// # Skipping cheaply
//
// A repository whose pushed_at predates the window cannot contain commits in it,
// so one request settles it and no branch is listed at all.
func ScanRepo(ctx context.Context, c *Client, fullName, author string, since, until time.Time, cachedBranches []string) (RepoScan, error) {
	repo, err := c.Repo(ctx, fullName)
	if err != nil {
		return RepoScan{FullName: fullName}, err
	}
	// pushed_at is the last push to ANY branch, so this is a sound filter: if
	// the newest push predates the window, nothing inside it can be in range.
	if !repo.PushedAt.IsZero() && repo.PushedAt.Before(since) {
		return RepoScan{FullName: fullName, Skipped: true}, nil
	}

	branches := cachedBranches
	if len(branches) == 0 {
		branches, err = c.Branches(ctx, fullName)
		if err != nil {
			return RepoScan{FullName: fullName}, err
		}
	}
	// A repository with no branches at all (freshly created, never pushed) still
	// deserves one default-branch attempt rather than being reported as empty.
	if len(branches) == 0 {
		branches = []string{repo.DefaultBranch}
	}

	seen := make(map[string]struct{})
	out := make([]Commit, 0, 16)
	for _, branch := range branches {
		commits, cerr := c.Commits(ctx, fullName, branch, author, since, until)
		if cerr != nil {
			return RepoScan{FullName: fullName}, cerr
		}
		for _, cm := range commits {
			if _, dup := seen[cm.SHA]; dup {
				continue
			}
			seen[cm.SHA] = struct{}{}
			out = append(out, cm)
		}
	}
	return RepoScan{FullName: fullName, Commits: out, Branches: branches}, nil
}
