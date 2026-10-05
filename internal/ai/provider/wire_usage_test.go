package provider

import (
	"encoding/json"
	"testing"
)

func TestCanonicalizeUsageCapturesCacheCreation(t *testing.T) {
	state := &attemptState{}
	raw := json.RawMessage(`{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":40,"cache_creation_tokens":25}}`)
	encoded := canonicalizeUsage(raw, state)
	if state.cacheCreationTokens() != 25 {
		t.Fatalf("state cache creation = %d, want 25", state.cacheCreationTokens())
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	var details map[string]uint64
	if err := json.Unmarshal(object["prompt_tokens_details"], &details); err != nil {
		t.Fatal(err)
	}
	if details["cache_creation_tokens"] != 25 || details["cached_tokens"] != 40 {
		t.Fatalf("details = %+v", details)
	}
}

func TestCanonicalizeUsageWithoutCacheCreation(t *testing.T) {
	state := &attemptState{}
	raw := json.RawMessage(`{"prompt_tokens":10,"completion_tokens":5}`)
	if encoded := canonicalizeUsage(raw, state); state.cacheCreationTokens() != 0 || string(encoded) == "" {
		t.Fatalf("unexpected mutation: %d %s", state.cacheCreationTokens(), encoded)
	}
}
