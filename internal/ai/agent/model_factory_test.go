package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/store"
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
