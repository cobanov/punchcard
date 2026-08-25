package http

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate the OpenAPI golden file (docs/openapi.json)")

// TestOpenAPISpec asserts the API emits a valid OpenAPI 3.1 document and that
// the committed docs/openapi.json is in sync. Regenerate with
// `make openapi` (which passes -update).
func TestOpenAPISpec(t *testing.T) {
	_, api := BuildRouter(testDeps())

	if v := api.OpenAPI().OpenAPI; v != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", v)
	}

	raw, err := json.Marshal(api.OpenAPI())
	if err != nil {
		t.Fatalf("marshal OpenAPI: %v", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatalf("indent OpenAPI: %v", err)
	}
	pretty.WriteByte('\n')

	goldenPath := filepath.Join("..", "..", "docs", "openapi.json")

	if *update {
		if err := os.WriteFile(goldenPath, pretty.Bytes(), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s (run `make openapi` to generate): %v", goldenPath, err)
	}
	if !bytes.Equal(want, pretty.Bytes()) {
		t.Fatalf("docs/openapi.json is out of sync with the code; run `make openapi`")
	}
}
