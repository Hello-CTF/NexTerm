package provider

import (
	"context"
	"testing"
)

func TestEinoChatModelConstruction(t *testing.T) {
	client, err := NewClient(Config{
		BaseURL: "http://127.0.0.1:8080/v1", APIKey: "key", Model: "m", Temperature: ptr(0.4),
	})
	if err != nil {
		t.Fatal(err)
	}
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if chatModel == nil {
		t.Fatal("ChatModel returned nil")
	}
	baseModel, contextWindow, err := client.BaseChatModel(context.Background())
	if err != nil || baseModel == nil || contextWindow != 1000 {
		t.Fatalf("BaseChatModel() = %v, %d, %v", baseModel, contextWindow, err)
	}
}
