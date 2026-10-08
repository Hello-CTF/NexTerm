package hitl

import (
	"encoding/json"
	"testing"
)

func TestWithNonceInjectsConfirmationNonce(t *testing.T) {
	got := WithNonce(json.RawMessage(`{"keys":"ls","enter":true}`), "nonce-1")
	var fields map[string]any
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["confirmationNonce"] != "nonce-1" {
		t.Fatalf("confirmationNonce = %v", fields["confirmationNonce"])
	}
	if fields["keys"] != "ls" || fields["enter"] != true {
		t.Fatalf("existing fields lost: %v", fields)
	}
}

func TestWithNonceToleratesEmptyAndInvalidArgs(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`not-json`)} {
		got := WithNonce(raw, "nonce-2")
		var fields map[string]any
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatalf("WithNonce(%q) = %q: %v", raw, got, err)
		}
		if len(fields) != 1 || fields["confirmationNonce"] != "nonce-2" {
			t.Fatalf("WithNonce(%q) = %s", raw, got)
		}
	}
}
