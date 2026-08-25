package service

import "math"

// i16 converts a non-negative, bounded int to int16, saturating at the limits.
// Callers pass already-validated values (e.g. priority 0-3); the guards make the
// conversion provably overflow-free.
func i16(n int) int16 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt16 {
		return math.MaxInt16
	}
	return int16(n)
}

// i32 converts a bounded int to int32, saturating at the limits.
func i32(n int) int32 {
	if n < math.MinInt32 {
		return math.MinInt32
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}
