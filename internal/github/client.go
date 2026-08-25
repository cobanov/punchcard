// Package github is the REST client and the commit scanner behind punchcard's
// one distinguishing feature: attaching the commits behind a stretch of work to
// the record of that work.
//
// Read scan.go before changing anything here. The scanner walks every branch on
// purpose, and the reason is not visible from the endpoint it calls.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is api.github.com. Tests point this at a local fake.
const DefaultBaseURL = "https://api.github.com"

// Errors the caller has to tell apart.
//
// ErrUnauthorized means the token is gone for good — revoked, expired, or its
// scope withdrawn — and retrying will not help; the user has to reconnect.
// ErrRateLimited and a transient server error both mean "try later". Collapsing
// the two would either spam GitHub with a dead token or silently stop scanning
// for a user whose token is fine.
var (
	ErrUnauthorized = errors.New("github: token rejected")
	ErrRateLimited  = errors.New("github: rate limited")
	ErrNotFound     = errors.New("github: not found")
)

// Client is a minimal GitHub REST client: the four calls the scanner needs and
// nothing else.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
}

// New builds a client. baseURL has no trailing slash.
func New(httpClient *http.Client, baseURL, token string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{http: httpClient, baseURL: strings.TrimRight(baseURL, "/"), token: token}
}

// Repo is the sliver of a repository the scanner uses.
type Repo struct {
	FullName      string    `json:"full_name"`
	DefaultBranch string    `json:"default_branch"`
	PushedAt      time.Time `json:"pushed_at"`
	Private       bool      `json:"private"`
}

// Commit is one commit as the scanner stores it.
type Commit struct {
	SHA         string
	Message     string
	URL         string
	CommittedAt time.Time
}

// Viewer is the authenticated user.
type Viewer struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// Me returns the authenticated user, which is where the scanner's author filter
// comes from.
func (c *Client) Me(ctx context.Context) (Viewer, error) {
	var v Viewer
	if err := c.get(ctx, "/user", &v); err != nil {
		return Viewer{}, err
	}
	return v, nil
}

// Repo fetches one repository. Its pushed_at is what lets the scanner skip a
// repository nobody touched during the window without listing anything.
func (c *Client) Repo(ctx context.Context, fullName string) (Repo, error) {
	var r Repo
	if err := c.get(ctx, "/repos/"+fullName, &r); err != nil {
		return Repo{}, err
	}
	return r, nil
}

// Branches lists a repository's branch names, following pagination.
func (c *Client) Branches(ctx context.Context, fullName string) ([]string, error) {
	var out []string
	path := fmt.Sprintf("/repos/%s/branches?per_page=100", fullName)
	for path != "" {
		var page []struct {
			Name string `json:"name"`
		}
		next, err := c.getPage(ctx, path, &page)
		if err != nil {
			return nil, err
		}
		for _, b := range page {
			out = append(out, b.Name)
		}
		path = next
	}
	return out, nil
}

// commitPayload is GitHub's commit shape, trimmed to what is stored.
type commitPayload struct {
	SHA    string `json:"sha"`
	URL    string `json:"html_url"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
		Author struct {
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

// Commits lists an author's commits on one branch within a window.
//
// `branch` goes into the `sha` parameter. Leaving it empty is what makes GitHub
// walk only the default branch — see scan.go for why that must never happen by
// accident.
func (c *Client) Commits(ctx context.Context, fullName, branch, author string, since, until time.Time) ([]Commit, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	q.Set("since", since.UTC().Format(time.RFC3339))
	q.Set("until", until.UTC().Format(time.RFC3339))
	if branch != "" {
		q.Set("sha", branch)
	}
	if author != "" {
		q.Set("author", author)
	}
	path := fmt.Sprintf("/repos/%s/commits?%s", fullName, q.Encode())

	var out []Commit
	for path != "" {
		var page []commitPayload
		next, err := c.getPage(ctx, path, &page)
		if err != nil {
			// A repository with no commits on a branch answers 409 "Git
			// Repository is empty"; an unknown branch answers 404. Neither is a
			// failure of the scan.
			if errors.Is(err, ErrNotFound) {
				return out, nil
			}
			return nil, err
		}
		for _, p := range page {
			when := p.Commit.Committer.Date
			if when.IsZero() {
				when = p.Commit.Author.Date
			}
			out = append(out, Commit{
				SHA: p.SHA, Message: p.Commit.Message, URL: p.URL, CommittedAt: when.UTC(),
			})
		}
		path = next
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	_, err := c.getPage(ctx, path, into)
	return err
}

// getPage performs one request and returns the path of the next page, taken
// from the Link header, or "" when there is none.
func (c *Client) getPage(ctx context.Context, path string, into any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("github request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		// 403 is both "rate limited" and "you may not read this". The remaining
		// counter is what separates them, and treating a quota pause as a dead
		// token would disconnect a user who did nothing wrong.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return "", ErrRateLimited
		}
		return "", ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", ErrRateLimited
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusConflict:
		return "", ErrNotFound
	case resp.StatusCode >= 300:
		return "", fmt.Errorf("github: unexpected status %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return "", fmt.Errorf("github: decode: %w", err)
	}
	return nextPagePath(resp.Header.Get("Link"), c.baseURL), nil
}

// nextPagePath pulls rel="next" out of a Link header and returns it as a path
// relative to baseURL.
func nextPagePath(link, baseURL string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 || !strings.Contains(segs[1], `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		if after, ok := strings.CutPrefix(raw, baseURL); ok {
			return after
		}
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return u.RequestURI()
	}
	return ""
}
