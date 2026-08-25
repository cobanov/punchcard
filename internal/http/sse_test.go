package http

import (
	"bufio"
	"context"
	"net/http"
	"strings"
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

// The multi-client SSE test that lived here exercised list membership: a shared
// list, a viewer added and then revoked mid-stream. punchcard has no sharing,
// so that invariant no longer exists. Its replacement — a stream carries the
// account's own events and no one else's — arrives with the domain events in
// internal/http/session_test.go.
