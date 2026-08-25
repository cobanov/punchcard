// Package cli is the punchcard command-line client.
//
// It talks to the same public API any other client would, with a bearer token —
// there is no privileged path. If something is awkward here, it is awkward for
// every client, and the fix belongs in the API rather than in a special case.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sentinel conditions the commands treat as ordinary states rather than
// failures. Both have exactly one sensible next step, and saying which one is
// most of a CLI's usefulness.
var (
	ErrNotLoggedIn      = errors.New("not signed in — run: punchcard login")
	ErrNoRunningSession = errors.New("no timer running")
)

// Client is an authenticated API client.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client for baseURL with a bearer token.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Project is a project as the CLI needs it.
type Project struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Client          string `json:"client,omitempty"`
	Currency        string `json:"currency"`
	HourlyRateCents *int64 `json:"hourly_rate_cents"`
	Billable        bool   `json:"billable"`
}

// Session is a work session as the CLI needs it.
type Session struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Note      string     `json:"note"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Seconds   int64      `json:"seconds"`
	Running   bool       `json:"running"`
	SyncState string     `json:"commit_sync_state"`
	SyncError string     `json:"commit_sync_error,omitempty"`
}

// Commit is a commit attributed to a session.
type Commit struct {
	SHA         string    `json:"sha"`
	Repo        string    `json:"repo"`
	Message     string    `json:"message"`
	CommittedAt time.Time `json:"committed_at"`
	URL         string    `json:"url,omitempty"`
}

// Projects lists the caller's projects.
func (c *Client) Projects(includeArchived bool) ([]Project, error) {
	path := "/v1/projects"
	if includeArchived {
		path += "?include_archived=true"
	}
	var out struct {
		Projects []Project `json:"projects"`
	}
	if err := c.do(http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

// CreateProject makes a project. rateCents is nil for a project with no rate,
// which is not the same as a rate of zero.
func (c *Client) CreateProject(name, client, currency string, rateCents *int64) (Project, error) {
	body := map[string]any{"name": name}
	if client != "" {
		body["client"] = client
	}
	if currency != "" {
		body["currency"] = currency
	}
	if rateCents != nil {
		body["hourly_rate_cents"] = *rateCents
	}
	var p Project
	if err := c.do(http.MethodPost, "/v1/projects", body, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

// LinkRepo attaches a GitHub repository to a project.
func (c *Client) LinkRepo(projectID, fullName string) error {
	return c.do(http.MethodPost, "/v1/projects/"+projectID+"/repos",
		map[string]any{"full_name": fullName}, nil)
}

// Start opens a timer, closing whatever was running.
func (c *Client) Start(projectID, note string) (Session, error) {
	var ws Session
	body := map[string]any{"project_id": projectID, "note": note}
	if err := c.do(http.MethodPost, "/v1/sessions", body, &ws); err != nil {
		return Session{}, err
	}
	return ws, nil
}

// Stop closes a session.
func (c *Client) Stop(sessionID string) (Session, error) {
	var ws Session
	if err := c.do(http.MethodPost, "/v1/sessions/"+sessionID+"/stop", map[string]any{}, &ws); err != nil {
		return Session{}, err
	}
	return ws, nil
}

// Current returns the running session, or ErrNoRunningSession.
func (c *Client) Current() (Session, error) {
	var ws Session
	if err := c.do(http.MethodGet, "/v1/sessions/current", nil, &ws); err != nil {
		return Session{}, err
	}
	return ws, nil
}

// Sessions lists sessions overlapping a window.
func (c *Client) Sessions(from, to time.Time) ([]Session, error) {
	q := url.Values{}
	q.Set("from", from.UTC().Format(time.RFC3339))
	q.Set("to", to.UTC().Format(time.RFC3339))
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.do(http.MethodGet, "/v1/sessions?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// Commits returns a session's attributed commits.
func (c *Client) Commits(sessionID string) ([]Commit, error) {
	var out struct {
		Commits []Commit `json:"commits"`
	}
	if err := c.do(http.MethodGet, "/v1/sessions/"+sessionID+"/commits", nil, &out); err != nil {
		return nil, err
	}
	return out.Commits, nil
}

// GitHubStatus is the connection state, for `punchcard status`.
type GitHubStatus struct {
	Connected bool   `json:"connected"`
	Login     string `json:"login,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// GitHub reports the GitHub connection.
func (c *Client) GitHub() (GitHubStatus, error) {
	var st GitHubStatus
	if err := c.do(http.MethodGet, "/v1/github/status", nil, &st); err != nil {
		return GitHubStatus{}, err
	}
	return st, nil
}

// problem is the RFC 9457 body the API returns for every error.
type problem struct {
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
	Title  string `json:"title"`
}

// do performs one request and decodes the response.
//
// Errors keep the server's own wording. "GitHub rejected the stored token;
// reconnect GitHub" tells the user what to do; "request failed: 500" does not,
// and the API already went to the trouble of saying the first thing.
func (c *Client) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		var p problem
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		_ = json.Unmarshal(raw, &p)
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return ErrNotLoggedIn
		case resp.StatusCode == http.StatusNotFound && strings.HasSuffix(path, "/current"):
			return ErrNoRunningSession
		case p.Detail != "":
			return errors.New(p.Detail)
		default:
			return fmt.Errorf("%s %s: %s", method, path, resp.Status)
		}
	}
	if out == nil {
		return nil
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("could not read the response: %w", err)
	}
	return nil
}

// RecordAgentRuns reports a batch of agent working intervals.
//
// The reply separates rows the server had never seen from ones it already had,
// because the queue is flushed at-least-once and "already known" is a success.
func (c *Client) RecordAgentRuns(runs []QueuedRun) (accepted, duplicates int, err error) {
	var out struct {
		Accepted   int `json:"accepted"`
		Duplicates int `json:"duplicates"`
	}
	if err := c.do(http.MethodPost, "/v1/agent-runs", map[string]any{"runs": runs}, &out); err != nil {
		return 0, 0, err
	}
	return out.Accepted, out.Duplicates, nil
}
