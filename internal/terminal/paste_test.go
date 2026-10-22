package terminal

import (
	"bytes"
	"testing"
)

func TestEncodePasteNewlinesAndBracketing(t *testing.T) {
	const input = "a\r\nb\rc\nd\r\n"
	if got, want := EncodePaste(input, false), []byte("a\rb\rc\rd\r"); !bytes.Equal(got, want) {
		t.Fatalf("plain paste = %q, want %q", got, want)
	}
	if got, want := EncodePaste(input, true), []byte("\x1b[200~"+input+"\x1b[201~"); !bytes.Equal(got, want) {
		t.Fatalf("bracketed paste = %q, want %q", got, want)
	}
}
