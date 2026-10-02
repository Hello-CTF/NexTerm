package usage

import (
	"encoding/json"
	"testing"
)

func TestModelAttributionAndMixedAccumulation(t *testing.T) {
	value := Usage{RunID: "run-1", CallID: "call-1", Model: "primary", PromptTokens: 3, CompletionTokens: 2}
	value.Accumulate(Usage{RunID: "run-1", CallID: "call-1", Model: "primary", PromptTokens: 4, CompletionTokens: 1})
	if value.RunID != "run-1" || value.CallID != "call-1" || value.Model != "primary" || value.PromptTokens != 7 || value.CompletionTokens != 3 {
		t.Fatalf("same-call accumulation = %+v", value)
	}
	value.Accumulate(Usage{RunID: "run-1", CallID: "call-2", Model: "fallback", PromptTokens: 5})
	if value.RunID != "run-1" || value.CallID != "" || value.Model != "" || !value.mixedModels || !value.mixedCallIDs || value.mixedRunIDs {
		t.Fatalf("mixed calls were misattributed: %+v", value)
	}
	value.Accumulate(Usage{RunID: "run-2", CallID: "call-3", Model: "primary", PromptTokens: 1})
	if value.RunID != "" || value.CallID != "" || value.Model != "" || !value.mixedRunIDs || !value.mixedCallIDs || !value.mixedModels {
		t.Fatalf("mixed attribution was restored: %+v", value)
	}
	encoded, err := json.Marshal(Usage{RunID: "run-1", CallID: "call-1", Model: "served-model", PromptTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"runId":"run-1","callId":"call-1","model":"served-model","promptTokens":1,"completionTokens":0,"cachedTokens":0,"contextWindow":0}` {
		t.Fatalf("model usage JSON = %s", encoded)
	}
	encoded, err = json.Marshal(Usage{PromptTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"promptTokens":1,"completionTokens":0,"cachedTokens":0,"contextWindow":0}` {
		t.Fatalf("model-free usage JSON changed: %s", encoded)
	}
}
