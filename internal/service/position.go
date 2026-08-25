package service

import "strings"

// Fractional ordering for manual task sort. Positions are strings
// over an ASCII-ordered base-62 alphabet, so lexicographic comparison matches
// intended order and a new key can always be generated strictly between any two
// existing keys without renumbering siblings.

const posAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const posBase = len(posAlphabet)

func posVal(c byte) int {
	// alphabet is contiguous-ish but not in ASCII; do a lookup.
	for i := 0; i < posBase; i++ {
		if posAlphabet[i] == c {
			return i
		}
	}
	return 0
}

// MaxPositionLen bounds a stored key. Generated keys grow by one character per
// repeated insert at the same spot, so this is generous for any real board
// while keeping a hostile client from storing a megabyte in the column.
const MaxPositionLen = 256

// ValidPosition reports whether s is a well-formed position key.
//
// The last rule is the load-bearing one, and it is not arbitrary. A key ending
// in the alphabet's lowest digit cannot be preceded: for hi == "0" there is no
// string K with "" < K < "0", because every character is >= '0', and any K
// starting with '0' has "0" as a prefix and therefore sorts after it. Asked for
// one anyway, PositionBetween returns "0V" — which sorts after "0", not before.
//
// PositionBetween never generates such a key (the character it returns on is
// alphabet[mid] with mid > loD >= 0, so the last character is always >= '1'), so
// the invariant holds for anything the server produces. It can only be broken by
// a client supplying a position directly, which the API allows. Rejecting those
// here is what keeps the algorithm total everywhere else.
func ValidPosition(s string) bool {
	if s == "" || len(s) > MaxPositionLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(posAlphabet, rune(s[i])) {
			return false
		}
	}
	return s[len(s)-1] != posAlphabet[0]
}

// PositionBetween returns a key strictly between lo and hi in lexicographic
// order. lo == "" means "before everything"; hi == "" means "after
// everything". It assumes lo < hi when both are non-empty, and that hi (when
// non-empty) satisfies ValidPosition — see there for why the second
// precondition exists and what happens when it is violated.
func PositionBetween(lo, hi string) string {
	var out []byte
	i := 0
	for {
		loD := 0
		if i < len(lo) {
			loD = posVal(lo[i])
		}
		hiD := posBase
		if i < len(hi) {
			hiD = posVal(hi[i])
		}

		if loD == hiD {
			out = append(out, posAlphabet[loD])
			i++
			continue
		}
		mid := (loD + hiD) / 2
		if mid == loD {
			// Digits are adjacent: keep lo's digit and recurse deeper, treating
			// hi as "past the end" for subsequent positions.
			out = append(out, posAlphabet[loD])
			hi = ""
			i++
			continue
		}
		out = append(out, posAlphabet[mid])
		return string(out)
	}
}
