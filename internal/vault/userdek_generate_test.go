package vault

import (
	"bytes"
	"testing"
)

func TestGenerateUserDEKProvidesIndependentKeyMaterial(t *testing.T) {
	first := GenerateUserDEK()
	second := GenerateUserDEK()
	defer func() {
		clear(first)
		clear(second)
	}()
	if len(first) != DEKLength || len(second) != DEKLength {
		t.Fatalf("DEK lengths = %d and %d, want %d", len(first), len(second), DEKLength)
	}
	if bytes.Equal(first, make([]byte, DEKLength)) || bytes.Equal(second, make([]byte, DEKLength)) {
		t.Fatal("generated DEK is all zero")
	}
	if bytes.Equal(first, second) {
		t.Fatal("independent calls generated the same DEK")
	}
}
