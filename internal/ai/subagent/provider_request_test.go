package subagent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
)

// TestAcceptanceTemperatureDefaultAndConfigured pins the
// NEXTERM_AI_TEMPERATURE contract shared with the agent acceptance tests:
// unset keeps the deterministic 0, a configured value is parsed verbatim.
func TestAcceptanceTemperatureDefaultAndConfigured(t *testing.T) {
	t.Setenv("NEXTERM_AI_TEMPERATURE", "")
	if got := acceptanceTemperature(t); got != 0 {
		t.Fatalf("default temperature = %v, want 0", got)
	}
	t.Setenv("NEXTERM_AI_TEMPERATURE", "1")
	if got := acceptanceTemperature(t); got != 1 {
		t.Fatalf("configured temperature = %v, want 1", got)
	}
	t.Setenv("NEXTERM_AI_TEMPERATURE", "1.25")
	if got := acceptanceTemperature(t); got != 1.25 {
		t.Fatalf("configured temperature = %v, want 1.25", got)
	}
}

// TestProfileModelFactorySendsConfiguredTemperature drives the production
// subagent path — profiles manager → NewProfileModelFactory → subagent
// manager spawn/wait — against a local HTTP server and asserts the active
// profile's temperature arrives verbatim in the chat completion request
// body. It is offline, deterministic, and credential-free by construction.
func TestProfileModelFactorySendsConfiguredTemperature(t *testing.T) {
	const configured = 1.25
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	manager, err := profiles.NewManager(ctx, newFakeSettings())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(ctx, profiles.Profile{
		BaseURL: server.URL, Model: "test-model", Temperature: configured, ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}
	subagents, err := subagent.NewManager(subagent.Config{
		NewModel:       subagent.NewProfileModelFactory(manager),
		MaxIterations:  2,
		MaxRunTime:     10 * time.Second,
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subagents.Close() }()
	handle, err := subagents.Spawn(ctx, subagent.Request{Task: "这是一次验收测试。不要调用任何工具，只回复 OK。", Scope: &subagent.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := subagents.Wait(ctx, handle)
	if err != nil {
		t.Fatalf("profile-backed subagent run failed: %v (%s)", err, result.Error)
	}
	if result.Status != subagent.StatusCompleted || result.Output != "OK" {
		t.Fatalf("result = %+v", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("chat completion requests = %d, want 1", len(bodies))
	}
	if got := bodies[0]["temperature"]; got != configured {
		t.Fatalf("request temperature = %v, want %v (body %v)", got, configured, bodies[0])
	}
	if got := bodies[0]["model"]; got != "test-model" {
		t.Fatalf("request model = %v, want test-model", got)
	}
}
