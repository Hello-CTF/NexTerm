package profiles

import (
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
)

func TestProfileJSONTemperatureAndReasoningEffortCompat(t *testing.T) {
	var legacy Profile
	if err := json.Unmarshal([]byte(`{"id":"p1","name":"legacy","baseUrl":"https://a.test/v1","model":"m","temperature":0.3,"contextWindow":1000,"proxy":null,"stream":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Temperature == nil || *legacy.Temperature != 0.3 {
		t.Fatalf("explicit 0.3 must stay explicit: %+v", legacy)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["temperature"] != 0.3 {
		t.Fatalf("round-trip lost explicit temperature: %s", encoded)
	}

	var withoutTemperature Profile
	if err := json.Unmarshal([]byte(`{"id":"p2","name":"old","baseUrl":"https://a.test/v1","model":"m","contextWindow":1000,"proxy":null,"stream":true}`), &withoutTemperature); err != nil {
		t.Fatal(err)
	}
	if withoutTemperature.Temperature != nil {
		t.Fatalf("missing temperature must stay unset, got %v", *withoutTemperature.Temperature)
	}
	encoded, err = json.Marshal(withoutTemperature)
	if err != nil {
		t.Fatal(err)
	}
	wire = nil
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["temperature"]; exists {
		t.Fatalf("unset temperature must be omitted: %s", encoded)
	}
	if _, exists := wire["reasoningEffort"]; exists {
		t.Fatalf("unset reasoning effort must be omitted: %s", encoded)
	}

	var withEffort Profile
	if err := json.Unmarshal([]byte(`{"id":"p3","model":"m","temperature":0,"reasoningEffort":"low"}`), &withEffort); err != nil {
		t.Fatal(err)
	}
	if withEffort.Temperature == nil || *withEffort.Temperature != 0 || withEffort.ReasoningEffort != provider.ReasoningEffortLow {
		t.Fatalf("explicit zero/effort were lost: %+v", withEffort)
	}

	normalized := Profile{Temperature: profileFloat64Ptr(9), ReasoningEffort: "max"}.Normalized()
	if normalized.Temperature == nil || *normalized.Temperature != 2 || normalized.ReasoningEffort != "" {
		t.Fatalf("normalized = %+v", normalized)
	}
}

func profileFloat64Ptr(value float64) *float64 { return &value }
