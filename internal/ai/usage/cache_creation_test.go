package usage

import (
	"encoding/json"
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
