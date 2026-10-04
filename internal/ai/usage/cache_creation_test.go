package usage

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseCacheCreationTokens(t *testing.T) {
	cases := map[string]string{
		"openai details":      `{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":40,"cache_creation_tokens":25}}`,
		"anthropic top":       `{"input_tokens":100,"output_tokens":10,"cache_creation_input_tokens":25,"cache_read_input_tokens":40}`,
		"prompt cache pair":   `{"prompt_cache_hit_tokens":40,"prompt_cache_creation_tokens":25,"completion_tokens":10}`,
		"nested cache object": `{"prompt_tokens":100,"completion_tokens":10,"cache_creation":{"tokens":25}}`,
	}
	for name, raw := range cases {
		parsed := Parse(json.RawMessage(raw))
		if parsed.CacheCreationTokens != 25 {
			t.Fatalf("%s: cache creation = %d, want 25", name, parsed.CacheCreationTokens)
		}
	}
}

func TestAccumulateSumsCacheCreationAndLatency(t *testing.T) {
	var total Usage
	total.Accumulate(Usage{PromptTokens: 10, CacheCreationTokens: 5, LatencyMS: 120})
	total.Accumulate(Usage{PromptTokens: 3, CacheCreationTokens: 7, LatencyMS: 80})
	if total.CacheCreationTokens != 12 || total.LatencyMS != 200 || total.PromptTokens != 13 {
		t.Fatalf("unexpected totals: %+v", total)
	}
}

func TestSaturatingInt64(t *testing.T) {
	if got := SaturatingInt64(math.MaxUint64); got != math.MaxInt64 {
		t.Fatalf("SaturatingInt64(MaxUint64) = %d", got)
	}
	if got := SaturatingInt64(math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("SaturatingInt64(MaxInt64) = %d", got)
	}
	if got := SaturatingInt64(42); got != 42 {
		t.Fatalf("SaturatingInt64(42) = %d", got)
	}
}

func TestSaturatingAdd(t *testing.T) {
	if got := SaturatingAdd(math.MaxUint64-1, 10); got != math.MaxUint64 {
		t.Fatalf("SaturatingAdd overflow = %d", got)
	}
	if got := SaturatingAdd(1, 2); got != 3 {
		t.Fatalf("SaturatingAdd(1,2) = %d", got)
	}
}
