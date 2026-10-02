//go:build windows

package vault

import (
	"bytes"
	"testing"
)

func TestDPAPIRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	protected, err := systemProtect(key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := systemUnprotect(protected)
	if err != nil || !bytes.Equal(opened, key) {
		t.Fatalf("DPAPI round trip opened=%x err=%v", opened, err)
	}
	if _, err := systemUnprotect([]byte("not-a-dpapi-blob")); err == nil {
		t.Fatal("invalid DPAPI data must fail")
	}
}
