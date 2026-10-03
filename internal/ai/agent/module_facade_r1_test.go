package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestR1ConfigFacadesUseWrappersWithoutZeroingState(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	permissions, err := guard.NewManager(context.Background(), storage)
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(context.Background(), storage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profileManager.Save(context.Background(), profiles.Profile{BaseURL: "http://127.0.0.1:1/v1", Model: "primary", ContextWindow: 32000}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Permissions: permissions, Profiles: profileManager})
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_set_permission", Args: json.RawMessage(`{"config":{"mode":"read_only","dangerRules":["blocked"]}}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("set permission failed: %+v", response.Error)
	}
	config := permissions.Get()
	if config.Mode != guard.ReadOnly || len(config.DangerRules) != 1 || config.DangerRules[0] != "blocked" {
		t.Fatalf("permission config = %+v", config)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_set_permission", Args: json.RawMessage(`{}`)}, ipc.Environment{})
	if response.OK {
		t.Fatal("missing permission config accepted")
	}
	if unchanged := permissions.Get(); unchanged.Mode != config.Mode || len(unchanged.DangerRules) != 1 {
		t.Fatalf("missing config zeroed permissions: %+v", unchanged)
	}
}
