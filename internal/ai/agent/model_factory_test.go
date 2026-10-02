package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestActiveProfileModelUsesM31Factory(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	manager, err := profiles.NewManager(context.Background(), storage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(context.Background(), profiles.Profile{BaseURL: "http://127.0.0.1:1/v1", Model: "primary", FallbackModel: "fallback", ContextWindow: 64000, Stream: true}); err != nil {
		t.Fatal(err)
	}
	factory := activeProfileModel(manager)
	chatModel, window, err := factory(context.Background())
	if err != nil || chatModel == nil || window != 64000 {
		t.Fatalf("model=%v window=%d err=%v", chatModel, window, err)
	}
}

func TestLegacySetProviderPreservesFallbackModel(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	manager, err := profiles.NewManager(context.Background(), storage)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Profiles: manager})
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_set_provider", Args: json.RawMessage(`{"baseUrl":"http://127.0.0.1:1/v1","model":"primary","fallbackModel":"fallback","contextWindow":64000,"stream":true}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("set provider failed: %+v", response.Error)
	}
	profile, ok := manager.ActiveProfile()
	if !ok || profile.FallbackModel != "fallback" || profile.ContextWindow != 64000 {
		t.Fatalf("profile=%+v", profile)
	}
}
