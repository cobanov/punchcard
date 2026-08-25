package service

import "testing"

func TestPositionBetweenOrdering(t *testing.T) {
	first := PositionBetween("", "")
	second := PositionBetween(first, "")
	if first >= second {
		t.Fatalf("append order broken: %q !< %q", first, second)
	}
	before := PositionBetween("", first)
	if before >= first {
		t.Fatalf("prepend order broken: %q !< %q", before, first)
	}
	mid := PositionBetween(first, second)
	if first >= mid || mid >= second {
		t.Fatalf("midpoint broken: %q < %q < %q", first, mid, second)
	}
}

func TestPositionBetweenAdjacent(t *testing.T) {
	x := PositionBetween("V", "W")
	if "V" >= x || x >= "W" {
		t.Fatalf("adjacent broken: V < %q < W", x)
	}
}

// Repeatedly inserting at the same spot must keep strict ordering (no collisions).
func TestPositionBetweenRepeatedInsert(t *testing.T) {
	lo, hi := PositionBetween("", ""), PositionBetween(PositionBetween("", ""), "")
	prev := lo
	for i := 0; i < 100; i++ {
		k := PositionBetween(prev, hi)
		if prev >= k || k >= hi {
			t.Fatalf("iteration %d: %q < %q < %q broken", i, prev, k, hi)
		}
		prev = k
	}
}

// The invariant behind ValidPosition, asserted rather than asserted-in-a-comment:
// a key ending in the alphabet's lowest digit cannot be preceded, so if
// PositionBetween could ever generate one, "insert at the top" would be broken
// for that list forever after.
func TestGeneratedPositionsAreAlwaysValid(t *testing.T) {
	// Walk the two ways a key is generated in anger: appending at the end, and
	// repeatedly inserting at the very top (the "new task lands on top" path).
	end := ""
	top := ""
	for i := 0; i < 500; i++ {
		end = PositionBetween(end, "")
		if !ValidPosition(end) {
			t.Fatalf("append iteration %d generated an invalid key %q", i, end)
		}
		top = PositionBetween("", top)
		if !ValidPosition(top) {
			t.Fatalf("prepend iteration %d generated an invalid key %q", i, top)
		}
		if top >= end && i > 0 {
			t.Fatalf("iteration %d: prepended %q is not below appended %q", i, top, end)
		}
	}
}

// Every key ValidPosition accepts must actually be precedable — that is the
// whole point of the rule. Exhaustive over one- and two-character keys.
func TestValidPositionsCanAlwaysBePreceded(t *testing.T) {
	for i := 0; i < posBase; i++ {
		for j := -1; j < posBase; j++ {
			hi := string(posAlphabet[i])
			if j >= 0 {
				hi += string(posAlphabet[j])
			}
			if !ValidPosition(hi) {
				continue
			}
			if got := PositionBetween("", hi); got >= hi {
				t.Errorf("PositionBetween(%q, %q) = %q, which does not sort before %q", "", hi, got, hi)
			}
		}
	}
}

func TestValidPositionRejects(t *testing.T) {
	for _, bad := range []string{
		"",      // empty
		"0",     // ends in the lowest digit: nothing can precede it
		"V0",    // same, one level deeper
		"V-",    // '-' is outside the alphabet
		"héllo", // non-ASCII
		"V V",   // space
	} {
		if ValidPosition(bad) {
			t.Errorf("ValidPosition(%q) = true, want false", bad)
		}
	}
	for _, ok := range []string{"V", "0V", "zzzV", "1"} {
		if !ValidPosition(ok) {
			t.Errorf("ValidPosition(%q) = false, want true", ok)
		}
	}
}
