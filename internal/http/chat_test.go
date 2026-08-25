package http

import (
	"encoding/json"
	"net/http"
	"testing"
)

// `actions` is how the client tells an answer from a receipt: nothing changed,
// so the text IS the reply and has to stay up to be read. That question is
// asked as `res.actions.length`, which means the field may never arrive as
// `null` — and a Go slice that was only ever appended to is nil when nothing
// appended, which marshals as exactly that. The bug was invisible from Go: the
// struct says `[]string` either way.
//
// So this asserts the BYTES. Decoding into a []string would turn `null` and
// `[]` into the same nil slice and pass against the defect it exists to catch.
func TestChatActionsAreAnEmptyArrayOnTheWire(t *testing.T) {
	env := newAPIEnv(t)
	// Nothing armed, so the stub answers with plain text and calls no tool —
	// the shape of "summarise my daily list", where the model reads and
	// explains and the board is untouched.
	status, body := do(t, env.session, http.MethodPost, env.base+"/v1/chat",
		map[string]any{"message": "what is on my list", "list_id": env.listID}, testCSRF())
	must(t, "chat", status, http.StatusOK)

	if got := rawField(t, body, "actions"); got != "[]" {
		t.Fatalf("actions on the wire = %s, want []\nfull body: %s", got, body)
	}
}

// And the empty array is a normalisation, not a hardwiring: a turn that did
// change something still reports what it did. Without this, `[]string{}`
// returned unconditionally would satisfy the test above.
func TestChatActionsReportWhatWasDone(t *testing.T) {
	env := newAPIEnv(t)
	env.stubGemini(t, "create_task", map[string]any{"list_id": env.listID, "title": "Call the dentist"})

	status, body := do(t, env.session, http.MethodPost, env.base+"/v1/chat",
		map[string]any{"message": "add call the dentist", "list_id": env.listID}, testCSRF())
	must(t, "chat", status, http.StatusOK)

	var out struct {
		Actions []string `json:"actions"`
	}
	unmarshal(t, body, &out)
	if len(out.Actions) != 1 {
		t.Fatalf("actions = %v, want one entry\nfull body: %s", out.Actions, body)
	}
}

// rawField returns one top-level field of a JSON object still encoded, so a
// test can assert on what was actually sent rather than on what Go makes of it.
func rawField(t *testing.T, body []byte, name string) string {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("response is not a JSON object: %v\nbody: %s", err, body)
	}
	raw, ok := wire[name]
	if !ok {
		t.Fatalf("response has no %q field\nbody: %s", name, body)
	}
	return string(raw)
}
