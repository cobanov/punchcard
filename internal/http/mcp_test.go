package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	appmcp "github.com/cobanov/punchcard/internal/mcp"
)

type bearerRT struct {
	pat  string
	base http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.pat)
	return b.base.RoundTrip(r)
}

func structUnmarshal(t *testing.T, v, out any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

// TestMCPToolsOverHTTP exercises the MCP tools through the SDK client over the
// streamable HTTP transport, and asserts the excluded surface (tokens/webhooks/
// members/account) is not reachable.
func TestMCPToolsOverHTTP(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	ctx := context.Background()
	csrf := testCSRF()

	// Register and mint a read_write PAT via the REST API.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if st, _ := do(t, c, http.MethodPost, base+"/v1/auth/register",
		map[string]string{"email": "mcp@example.com", "password": "supersecret123"}, nil); st != http.StatusCreated {
		t.Fatalf("register: %d", st)
	}
	_, tb := do(t, c, http.MethodPost, base+"/v1/tokens",
		map[string]any{"name": "mcp", "scope": "read_write"}, csrf)
	var tok struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, tb, &tok)

	// Serve the MCP HTTP transport pointing at the REST base URL, then connect
	// an MCP client with the PAT.
	mcpSrv := httptest.NewServer(appmcp.HTTPHandler(base))
	t.Cleanup(mcpSrv.Close)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   mcpSrv.URL,
		HTTPClient: &http.Client{Transport: bearerRT{pat: tok.Secret, base: http.DefaultTransport}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Tool inventory: required present, excluded absent.
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{
		"todo_create_task", "todo_create_tasks", "todo_get_task", "todo_list_tasks",
		"todo_update_task", "todo_complete_task", "todo_delete_task", "todo_list_lists", "todo_create_list",
	} {
		if !names[want] {
			t.Errorf("missing MCP tool %q", want)
		}
	}
	for n := range names {
		for _, banned := range []string{"token", "webhook", "member", "invite", "session", "account", "password"} {
			if strings.Contains(n, banned) {
				t.Errorf("excluded operation exposed via MCP: %q", n)
			}
		}
	}

	// list_lists -> Inbox.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "todo_list_lists"})
	if err != nil || res.IsError {
		t.Fatalf("list_lists: err=%v result=%+v", err, res)
	}
	var ll struct {
		Lists []struct {
			ID string `json:"id"`
		} `json:"lists"`
	}
	structUnmarshal(t, res.StructuredContent, &ll)
	if len(ll.Lists) < 1 {
		t.Fatal("expected at least the Inbox list")
	}
	inbox := ll.Lists[0].ID

	// create_task.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "todo_create_task", Arguments: map[string]any{"list_id": inbox, "title": "via mcp", "priority": 2},
	})
	if err != nil || res.IsError {
		t.Fatalf("create_task: err=%v result=%+v", err, res)
	}

	// list_tasks shows it.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "todo_list_tasks", Arguments: map[string]any{"list_id": inbox}})
	if err != nil || res.IsError {
		t.Fatalf("list_tasks: err=%v", err)
	}
	var lt struct {
		Tasks []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"tasks"`
	}
	structUnmarshal(t, res.StructuredContent, &lt)
	if len(lt.Tasks) != 1 || lt.Tasks[0].Title != "via mcp" {
		t.Fatalf("expected 1 task 'via mcp', got %+v", lt.Tasks)
	}

	// complete_task.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "todo_complete_task", Arguments: map[string]any{"id": lt.Tasks[0].ID}})
	if err != nil || res.IsError {
		t.Fatalf("complete_task: err=%v result=%+v", err, res)
	}
}
