package agent

import (
	"math"
	"testing"
)

func TestClampTokensInt64(t *testing.T) {
	if got := clampTokensInt64(0); got != 0 {
		t.Fatalf("clampTokensInt64(0) = %d", got)
	}
	if got := clampTokensInt64(12345); got != 12345 {
		t.Fatalf("clampTokensInt64(12345) = %d", got)
	}
	if got := clampTokensInt64(math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("clampTokensInt64(MaxInt64) = %d", got)
	}
	if got := clampTokensInt64(math.MaxInt64 + 1); got != math.MaxInt64 {
		t.Fatalf("clampTokensInt64(MaxInt64+1) = %d", got)
	}
	if got := clampTokensInt64(math.MaxUint64); got != math.MaxInt64 {
		t.Fatalf("clampTokensInt64(MaxUint64) = %d", got)
	}
}
