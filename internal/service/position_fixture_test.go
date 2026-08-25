package service

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateFixtures = flag.Bool("update-positions", false, "rewrite the shared position fixture from this implementation")

// fixturePath is deliberately outside internal/ so the TypeScript port can read
// the same bytes. testdata/ would be idiomatic Go but unreachable from web/.
const fixturePath = "../../db/position_fixtures.json"

type positionCase struct {
	Lo   string `json:"lo"`
	Hi   string `json:"hi"`
	Want string `json:"want"`
	Why  string `json:"why"`
}

// The fractional-ordering algorithm exists twice: here, and as a hand port in
// web/src/offline/position.ts, because the client must compute a position for a
// drag or an offline create without a round-trip. That duplication is
// deliberate. What it needs is something that keeps the two honest.
//
// This file is the Go half. web/e2e/position.mjs asserts the same fixture
// against the TypeScript port. If either implementation is edited without the
// other, one of the two fails.
//
// The failure mode being guarded is quiet: positions decide render order, so
// drift means a reorder made offline lands somewhere different from the same
// reorder made online, and two clients converge on different boards with no
// error anywhere.
func TestPositionBetweenMatchesSharedFixture(t *testing.T) {
	if *updateFixtures {
		writeFixture(t)
		return
	}

	raw, err := os.ReadFile(filepath.Clean(fixturePath))
	if err != nil {
		t.Fatalf("read fixture: %v (regenerate with: go test ./internal/service -run SharedFixture -update-positions)", err)
	}
	var cases []positionCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("fixture is empty; this test would assert nothing")
	}

	for _, c := range cases {
		if got := PositionBetween(c.Lo, c.Hi); got != c.Want {
			t.Errorf("PositionBetween(%q, %q) = %q, fixture says %q  [%s]",
				c.Lo, c.Hi, got, c.Want, c.Why)
		}
	}
}

// inputs are the pairs worth pinning: both boundary conventions, the
// adjacent-digit recursion that is the algorithm's only subtle branch, and the
// alphabet's ends where an off-by-one in the base-62 mapping would show up.
var inputs = []positionCase{
	{Lo: "", Hi: "", Why: "empty board: first key"},
	{Lo: "V", Hi: "", Why: "append after the first key"},
	{Lo: "", Hi: "V", Why: "prepend before the first key"},
	{Lo: "V", Hi: "W", Why: "adjacent digits: forces the recurse-deeper branch"},
	{Lo: "V", Hi: "VV", Why: "prefix relationship"},
	{Lo: "0", Hi: "1", Why: "adjacent at the bottom of the alphabet"},
	{Lo: "y", Hi: "z", Why: "adjacent at the top of the alphabet"},
	{Lo: "", Hi: "0", Why: "PRECONDITION VIOLATION (see ValidPosition): nothing can precede a key ending in the lowest digit. The result is degenerate — it sorts AFTER hi. Pinned so the two languages are at least degenerate identically; ValidPosition now rejects such a hi at the write boundary."},
	{Lo: "z", Hi: "", Why: "append after the highest digit"},
	{Lo: "0", Hi: "z", Why: "full span"},
	{Lo: "VV", Hi: "VW", Why: "adjacent one level deep"},
	{Lo: "VVVV", Hi: "VVVW", Why: "adjacent three levels deep"},
	{Lo: "A", Hi: "a", Why: "case boundary: uppercase to lowercase is contiguous in this alphabet"},
	{Lo: "9", Hi: "A", Why: "digit-to-uppercase boundary"},
	{Lo: "Z", Hi: "a", Why: "uppercase-to-lowercase boundary"},
}

func writeFixture(t *testing.T) {
	t.Helper()
	out := make([]positionCase, len(inputs))
	for i, c := range inputs {
		c.Want = PositionBetween(c.Lo, c.Hi)
		out[i] = c
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Clean(fixturePath), append(body, '\n'), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Logf("wrote %d cases to %s", len(out), fixturePath)
}
