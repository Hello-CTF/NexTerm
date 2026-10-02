package ids

import (
	"testing"
	"time"
)

func TestNewULID(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id := New()
		if len(id) != 26 || !Valid(id) {
			t.Fatalf("invalid ULID %q", id)
		}
		if id[0] > '7' {
			t.Fatalf("ULID overflows 128 bits: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ULID %q", id)
		}
		seen[id] = true
	}
}

func TestValidMatchesIdentifierBoundary(t *testing.T) {
	for _, id := range []string{"", "short", "AAAAAAAAAAAAAAAAAAAAAAAAAA!", "AAAAAAAAAAAAAAAAAAAAAAAAA-"} {
		if Valid(id) {
			t.Errorf("accepted invalid ID %q", id)
		}
	}
	if !Valid("01J0NEXTERMLOCALDEVICE0001") {
		t.Error("built-in asset ID must remain valid")
	}
}

func TestNowMS(t *testing.T) {
	before := time.Now().UnixMilli()
	got := NowMS()
	after := time.Now().UnixMilli()
	if got < before || got > after {
		t.Fatalf("NowMS=%d outside [%d,%d]", got, before, after)
	}
}
