package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/activity"
	"github.com/cobanov/punchcard/internal/audit"
	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/gemini"
	"github.com/cobanov/punchcard/internal/observability"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/service"
	"github.com/cobanov/punchcard/internal/testutil"
	"github.com/cobanov/punchcard/internal/webhooks"
)

// originTestPassword is registerActor's hardcoded password (domain_test.go),
// duplicated here because a device-token login needs the same credential and
// registerActor does not return it.
const originTestPassword = "supersecret123"

// apiEnv is one registered user wired through BuildRouter exactly like
// production — a real router over a real Postgres — carrying the three
// credential shapes originOf has to tell apart: a session cookie, a
// read_write PAT, and a device token, all for the same account. A fake
// principal harness could hide a wiring mistake a real request path (cookie
// jar, Bearer header, CSRF) would catch.
//
// other/otherUserID is a second registered account. The activity log's
// authorization question — whose actions may this principal read — has no
// content with only one user ever acting, so the activity tests need a
// co-member distinct from the primary session throughout.
type apiEnv struct {
	pool        *testutil.Pool
	base        string
	userID      uuid.UUID
	listID      string
	session     *http.Client
	pat         *http.Client
	deviceToken *http.Client
	other       *http.Client
	otherUserID uuid.UUID
	stub        *geminiStub // the fake Gemini backend Deps.Gemini points at
}

// newAPIEnv provisions a fresh Postgres, builds the real router (mirroring
// newAuthTestServer in auth_test.go), and registers one user with all three
// credentials plus a list to act on.
func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	logger := observability.NewLogger("error")
	cfg := testConfig()
	auditor := audit.NewLogger(store, logger)
	sender := newCaptureSender()
	authSvc := service.NewAuth(store, sender, auditor, logger, cfg)
	whKey, _ := webhooks.GenerateKey()
	cipher, _ := webhooks.NewCipher(whKey)
	domainSvc := service.NewDomain(store, auditor, sender, cipher, logger, cfg)

	migrator, err := repo.NewMigrator(pool)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	t.Cleanup(func() { _ = migrator.Close() })

	// The fake Gemini endpoint has to exist before BuildRouter runs:
	// registerChatRoutes closes over Deps by value, so assigning d.Gemini
	// after the router is built would never be seen by the chat handler.
	// stubGemini (below) only ever changes what this stub answers, never
	// which client the handler holds.
	stub := &geminiStub{}
	geminiSrv := httptest.NewServer(stub)
	t.Cleanup(geminiSrv.Close)

	router, _ := BuildRouter(Deps{
		Config: cfg, Logger: logger, Metrics: observability.NewMetrics(),
		Pool: pool, Migrator: migrator, Store: store, Auth: authSvc, Domain: domainSvc,
		Gemini: gemini.NewWithEndpoint("test-key", "", geminiSrv.URL+"/"),
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	base := srv.URL

	email := fmt.Sprintf("origin-%s@example.com", uuid.NewString())
	session, meID := registerActor(t, base, email)
	userID := uuid.MustParse(meID)

	csrf := testCSRF()
	_, tokBody := do(t, session, http.MethodPost, base+"/v1/tokens",
		map[string]any{"name": "script", "scope": "read_write"}, csrf)
	var tok struct {
		Secret string `json:"secret"`
	}
	unmarshal(t, tokBody, &tok)
	pat := &http.Client{Transport: bearerRT{pat: tok.Secret, base: http.DefaultTransport}}

	deviceTok := deviceLogin(t, base, email, originTestPassword)
	deviceToken := &http.Client{Transport: bearerRT{pat: deviceTok, base: http.DefaultTransport}}

	otherEmail := fmt.Sprintf("origin-other-%s@example.com", uuid.NewString())
	other, otherMeID := registerActor(t, base, otherEmail)
	otherUserID := uuid.MustParse(otherMeID)

	_, listBody := do(t, session, http.MethodPost, base+"/v1/lists", map[string]string{"name": "Origins"}, csrf)
	var list struct {
		ID string `json:"id"`
	}
	unmarshal(t, listBody, &list)

	return &apiEnv{
		pool: pool, base: base, userID: userID, listID: list.ID,
		session: session, pat: pat, deviceToken: deviceToken,
		other: other, otherUserID: otherUserID, stub: stub,
	}
}

// deviceLogin signs an already-registered account into a device token — the
// second half of device_token_test.go's nativeToken, minus the registration:
// this env's account is registered once by newAPIEnv, so registering again
// here would just fail with "email taken".
func deviceLogin(t *testing.T, base, email, password string) string {
	t.Helper()
	st, body := do(t, &http.Client{}, http.MethodPost, base+"/v1/auth/native/login",
		map[string]string{"email": email, "password": password, "client_name": "test-device"}, nil)
	if st != http.StatusOK {
		t.Fatalf("native login = %d, want 200", st)
	}
	var out struct {
		Token string `json:"token"`
	}
	unmarshal(t, body, &out)
	if out.Token == "" {
		t.Fatal("native login returned no token")
	}
	return out.Token
}

// doWithHeader is do with one extra header, merged with a valid CSRF token so
// a session-authenticated call still passes authMiddleware's CSRF gate. A PAT
// or device-token call simply ignores the extra CSRF header, since neither is
// cookie-authenticated and the gate never looks at it for them.
func doWithHeader(t *testing.T, client *http.Client, method, url string, body any, key, value string) (int, []byte) {
	t.Helper()
	h := testCSRF()
	h[key] = value
	return do(t, client, method, url, body, h)
}

// latestActivityOrigin reads the origin column of this env's user's most
// recent activity row, straight from Postgres: there is no read endpoint yet
// (a later task), and internal/service/activity_write_test.go already
// established querying the table directly as the accepted way to assert on
// activity rows from a test.
func (e *apiEnv) latestActivityOrigin(t *testing.T) string {
	t.Helper()
	row := testutil.QueryRow(t, e.pool, `
		SELECT origin FROM activity WHERE user_id = $1
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, e.userID)
	var origin string
	if err := row.Scan(&origin); err != nil {
		t.Fatalf("latest activity origin: %v", err)
	}
	return origin
}

// stubGemini arms the fake Gemini endpoint wired into this env's router to
// answer the chat request's first turn with a functionCall for name/args —
// otherwise the chat handler has no key configured and Enabled() would
// short-circuit it to 503 before anything is labelled. See NewWithEndpoint in
// internal/gemini for why this seam exists.
func (e *apiEnv) stubGemini(t *testing.T, name string, args map[string]any) {
	t.Helper()
	e.stub.arm(name, args)
}

// geminiStub is a fake Gemini backend. Its first response is the armed tool
// call, if any; every response after that is plain text, so the chat loop's
// second turn finds no functionCall, stops, and does not call the same tool
// up to maxChatTurns times in a row.
type geminiStub struct {
	mu    sync.Mutex
	calls int
	name  string
	args  map[string]any
}

// geminiWireCandidate mirrors the unexported response type in internal/gemini
// closely enough to be decoded by it — only candidates[].content.parts, which
// is all the client reads.
type geminiWireCandidate struct {
	Content      gemini.Content `json:"content"`
	FinishReason string         `json:"finishReason"`
}

func (s *geminiStub) arm(name string, args map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = 0
	s.name = name
	s.args = args
}

func (s *geminiStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.calls++
	call, name, args := s.calls, s.name, s.args
	s.mu.Unlock()

	var part gemini.Part
	if call == 1 && name != "" {
		raw, err := json.Marshal(args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		part = gemini.Part{FunctionCall: &gemini.FunctionCall{Name: name, Args: raw}}
	} else {
		part = gemini.Part{Text: "Done."}
	}

	resp := struct {
		Candidates []geminiWireCandidate `json:"candidates"`
	}{Candidates: []geminiWireCandidate{{
		Content:      gemini.Content{Role: "model", Parts: []gemini.Part{part}},
		FinishReason: "STOP",
	}}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// The assistant runs as the user's own principal — that is the whole security
// story — so nothing about the credential distinguishes it. The origin has to
// be set in-process, and it must not be claimable from outside.
func TestChatMutationsAreLabelledAgent(t *testing.T) {
	env := newAPIEnv(t)
	env.stubGemini(t, "create_task", map[string]any{"list_id": env.listID, "title": "Call the dentist"})

	status, _ := do(t, env.session, http.MethodPost, env.base+"/v1/chat",
		map[string]any{"message": "add call the dentist", "list_id": env.listID}, testCSRF())
	must(t, "chat", status, http.StatusOK)

	if got := env.latestActivityOrigin(t); got != "agent" {
		t.Fatalf("origin: got %q, want agent", got)
	}
}

func TestPATMutationsAreLabelledAPI(t *testing.T) {
	env := newAPIEnv(t)
	status, _ := do(t, env.pat, http.MethodPost, env.base+"/v1/tasks",
		map[string]any{"list_id": env.listID, "title": "from a script"}, nil)
	must(t, "create via PAT", status, http.StatusCreated)

	if got := env.latestActivityOrigin(t); got != "api" {
		t.Fatalf("origin: got %q, want api", got)
	}
}

// A device token is the first party's own app on a phone that cannot hold a
// cookie. Labelling it "api" would call every action taken in the iOS app "a
// program".
func TestDeviceTokenMutationsAreLabelledUser(t *testing.T) {
	env := newAPIEnv(t)
	status, _ := do(t, env.deviceToken, http.MethodPost, env.base+"/v1/tasks",
		map[string]any{"list_id": env.listID, "title": "from the phone"}, nil)
	must(t, "create via device token", status, http.StatusCreated)

	if got := env.latestActivityOrigin(t); got != "user" {
		t.Fatalf("origin: got %q, want user", got)
	}
}

func TestMCPHeaderIsHonouredForAPATOnly(t *testing.T) {
	env := newAPIEnv(t)

	status, _ := doWithHeader(t, env.pat, http.MethodPost, env.base+"/v1/tasks",
		map[string]any{"list_id": env.listID, "title": "via mcp"},
		"X-Punchcard-Origin", "mcp")
	must(t, "create via MCP", status, http.StatusCreated)
	if got := env.latestActivityOrigin(t); got != "mcp" {
		t.Fatalf("PAT + header: got %q, want mcp", got)
	}

	// A session may not relabel itself, and nothing may claim "agent".
	status, _ = doWithHeader(t, env.session, http.MethodPost, env.base+"/v1/tasks",
		map[string]any{"list_id": env.listID, "title": "not really mcp"},
		"X-Punchcard-Origin", "mcp")
	must(t, "create via session", status, http.StatusCreated)
	if got := env.latestActivityOrigin(t); got != "user" {
		t.Fatalf("session + header: got %q, want user", got)
	}

	status, _ = doWithHeader(t, env.pat, http.MethodPost, env.base+"/v1/tasks",
		map[string]any{"list_id": env.listID, "title": "claiming agent"},
		"X-Punchcard-Origin", "agent")
	must(t, "create claiming agent", status, http.StatusCreated)
	if got := env.latestActivityOrigin(t); got != "api" {
		t.Fatalf("PAT claiming agent: got %q, want api", got)
	}
}

// TestOriginOf is the header matrix on its own: originOf is a pure function
// of (principal, request), with no database in it, so it can be checked far
// more exhaustively here than the four end-to-end tests above afford —
// including the corners (nil principal, header case, garbage values) that
// would be expensive to prove through a real HTTP round trip and are not
// required to be proven that way. The four tests above stay: they are what
// prove originOf is actually wired into authMiddleware, which this test
// cannot see at all.
func TestOriginOf(t *testing.T) {
	tokenID := uuid.New()
	pat := &auth.Principal{TokenID: &tokenID}
	device := &auth.Principal{TokenID: &tokenID, ViaDevice: true}
	session := &auth.Principal{ViaSession: true}

	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/tasks", nil)
		if header != "" {
			r.Header.Set(activity.HeaderOrigin, header)
		}
		return r
	}

	tests := []struct {
		name   string
		p      *auth.Principal
		header string
		want   activity.Origin
	}{
		{"nil principal", nil, "", activity.User},
		{"nil principal claiming mcp", nil, "mcp", activity.User},
		{"session, no header", session, "", activity.User},
		{"session claiming mcp", session, "mcp", activity.User},     // cannot relabel itself
		{"session claiming agent", session, "agent", activity.User}, // nothing can claim agent via header
		{"device token, no header", device, "", activity.User},
		{"device token claiming mcp", device, "mcp", activity.User}, // still "user", never "api" or "mcp"
		{"PAT, no header", pat, "", activity.API},
		{"PAT with mcp header", pat, "mcp", activity.MCP},
		{"PAT with agent header", pat, "agent", activity.API}, // nothing can claim agent
		{"PAT with garbage header", pat, "carrier-pigeon", activity.API},
		{"PAT with uppercase MCP header", pat, "MCP", activity.API}, // exact match only, no case-folding
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := originOf(tc.p, req(tc.header)); got != tc.want {
				t.Fatalf("originOf(%+v, header=%q) = %q, want %q", tc.p, tc.header, got, tc.want)
			}
		})
	}
}
