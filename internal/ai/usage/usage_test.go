package usage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizationGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "normalization_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
		Expected Usage           `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 4 {
		t.Fatalf("golden file has only %d cases", len(cases))
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			actual := Parse(test.Input)
			if actual != test.Expected {
				t.Fatalf("Parse() = %+v, want %+v", actual, test.Expected)
			}
			actualJSON, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			expectedJSON, err := json.Marshal(test.Expected)
			if err != nil {
				t.Fatal(err)
			}
			if string(actualJSON) != string(expectedJSON) {
				t.Fatalf("JSON = %s, want %s", actualJSON, expectedJSON)
			}
		})
	}
}

func TestAccumulate(t *testing.T) {
	value := Usage{PromptTokens: 150, CompletionTokens: 20, CachedTokens: 75, ContextWindow: 100}
	value.Accumulate(Usage{PromptTokens: 50, CompletionTokens: 5, CachedTokens: 25, ContextWindow: 64000})
	want := Usage{PromptTokens: 200, CompletionTokens: 25, CachedTokens: 100, ContextWindow: 64000}
	if value != want {
		t.Fatalf("Accumulate() = %+v, want %+v", value, want)
	}
	value.Accumulate(Usage{})
	if value.ContextWindow != 64000 {
		t.Fatal("zero context window replaced the configured window")
	}

	overflow := Usage{PromptTokens: math.MaxUint64 - 1, CompletionTokens: math.MaxUint64, CachedTokens: math.MaxUint64 - 2}
	overflow.Accumulate(Usage{PromptTokens: 10, CompletionTokens: 1, CachedTokens: 10})
	if overflow.PromptTokens != math.MaxUint64 || overflow.CompletionTokens != math.MaxUint64 || overflow.CachedTokens != math.MaxUint64 {
		t.Fatalf("saturating accumulation overflowed: %+v", overflow)
	}
}

func TestParseInvalidJSONAndHasData(t *testing.T) {
	if got := Parse(nil); got != (Usage{}) {
		t.Fatalf("Parse(nil) = %+v", got)
	}
	if got := Parse(json.RawMessage(`{"prompt_tokens":`)); got != (Usage{}) {
		t.Fatalf("Parse(invalid) = %+v", got)
	}
	if (Usage{CachedTokens: 10}).HasData() {
		t.Fatal("cached-only usage must not replace a complete usage frame")
	}
	if !(Usage{CompletionTokens: 1}).HasData() {
		t.Fatal("completion usage should be reported")
	}
}
