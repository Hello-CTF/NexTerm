package subagent_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
)

// TestRealProviderSpawnAcceptance drives the full production path —
// profiles manager → NewProfileModelFactory → subagent manager spawn/wait —
// against a real provider. It is skipped unless the provider environment is
// provided; a skip is not an acceptance result.
func TestRealProviderSpawnAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_PROVIDER") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_PROVIDER=1 and provider environment to run")
	}
	baseURL, modelName := os.Getenv("NEXTERM_AI_BASE_URL"), os.Getenv("NEXTERM_AI_MODEL")
	if baseURL == "" || modelName == "" {
		t.Fatal("NEXTERM_AI_BASE_URL and NEXTERM_AI_MODEL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	manager, err := profiles.NewManager(ctx, newFakeSettings())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(ctx, profiles.Profile{
		BaseURL: baseURL, APIKey: os.Getenv("NEXTERM_AI_API_KEY"), Model: modelName,
		Temperature: 0, ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}
	subagents, err := subagent.NewManager(subagent.Config{
		NewModel:       subagent.NewProfileModelFactory(manager),
		MaxIterations:  2,
		MaxRunTime:     60 * time.Second,
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subagents.Close() }()
	handle, err := subagents.Spawn(ctx, subagent.Request{
		Task:  "这是一次验收测试。不要调用任何工具，只回复 OK。",
		Scope: &subagent.Scope{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := subagents.Wait(ctx, handle)
	if err != nil {
		t.Fatalf("real provider subagent run failed: %v (%s)", err, result.Error)
	}
	if result.Status != subagent.StatusCompleted {
		t.Fatalf("unexpected status %q: %s", result.Status, result.Error)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Fatal("real provider subagent returned empty output")
	}
	t.Logf("real provider subagent output: %q", result.Output)
}
