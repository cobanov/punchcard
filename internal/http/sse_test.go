package http

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// streamEvents reads an SSE stream, sending "connected" once and each event's
// type thereafter, until ctx is cancelled or the stream closes.
func streamEvents(ctx context.Context, client *http.Client, url string, out chan<- string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == ": connected":
			trySend(ctx, out, "connected")
		case strings.HasPrefix(line, "event: "):
			trySend(ctx, out, strings.TrimPrefix(line, "event: "))
		}
	}
}

func trySend(ctx context.Context, out chan<- string, v string) {
	select {
	case out <- v:
	case <-ctx.Done():
	}
}

func waitFor(ch <-chan string, want string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case v := <-ch:
			if v == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// TestSSEMultiClient verifies live delivery to a shared-list member and that a
// revoked member stops receiving events mid-stream.
func TestSSEMultiClient(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	owner, _ := registerActor(t, base, "sse-owner@example.com")
	viewer, viewerID := registerActor(t, base, "sse-viewer@example.com")

	_, lb := do(t, owner, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Shared"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, lb, &list)
	L := list.ID
	must(t, "add viewer", st(t, owner, http.MethodPost, base+"/v1/lists/"+L+"/members",
		map[string]any{"user_id": viewerID, "role": "viewer"}, csrf), http.StatusNoContent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan string, 32)
	go streamEvents(ctx, viewer, base+"/v1/events/stream?list_id="+L, events)

	if !waitFor(events, "connected", 3*time.Second) {
		t.Fatal("SSE did not connect")
	}

	// Owner creates a task; the viewer's stream should receive task.created.
	must(t, "create task 1", st(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "t1"}, csrf), http.StatusCreated)
	if !waitFor(events, "task.created", 4*time.Second) {
		t.Fatal("viewer did not receive task.created over SSE")
	}

	// Revoke the viewer, then create another task.
	must(t, "remove viewer", st(t, owner, http.MethodDelete, base+"/v1/lists/"+L+"/members/"+viewerID, nil, csrf), http.StatusNoContent)
	// Let the removal take effect across a poll cycle, draining any queued events.
	time.Sleep(500 * time.Millisecond)
	for len(events) > 0 {
		<-events
	}
	must(t, "create task 2", st(t, owner, http.MethodPost, base+"/v1/tasks",
		map[string]any{"list_id": L, "title": "t2"}, csrf), http.StatusCreated)

	if waitFor(events, "task.created", 2*time.Second) {
		t.Fatal("revoked viewer still received events over SSE")
	}
}
