package production

import (
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAIConversationCreatePreservesScope(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, profileManager, database); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "ai_conversation_create", `{"title":"scoped","scope":{"sessionId":"ssh-1"}}`)
	var created conversationDTO
	requireStoreTestResponse(t, response, &created)
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(created.Scope, &wrapped); err != nil {
		t.Fatal(err)
	}
	inner, ok := wrapped["scope"]
	if !ok {
		t.Fatalf("created scope has no outer scope wrapper: %s", created.Scope)
	}
	var scopeFields map[string]any
	if err := json.Unmarshal(inner, &scopeFields); err != nil {
		t.Fatal(err)
	}
	if scopeFields["sessionId"] != "ssh-1" {
		t.Fatalf("caller scope dropped: %s", created.Scope)
	}

	listResponse := dispatchStoreTest(dispatcher, "ai_conversation_list", `null`)
	var listed []conversationDTO
	requireStoreTestResponse(t, listResponse, &listed)
	if len(listed) != 1 || string(listed[0].Scope) != string(created.Scope) {
		t.Fatalf("listed scope = %+v, created = %+v", listed, created)
	}

	plainResponse := dispatchStoreTest(dispatcher, "ai_conversation_create", `{"title":"plain"}`)
	var plain conversationDTO
	requireStoreTestResponse(t, plainResponse, &plain)
	var plainWrapped map[string]json.RawMessage
	if err := json.Unmarshal(plain.Scope, &plainWrapped); err != nil {
		t.Fatal(err)
	}
	if len(plainWrapped) != 1 || plainWrapped["scope"] == nil || string(plainWrapped["scope"]) != "null" {
		t.Fatalf("scope-less create must persist {\"scope\":null}, got %s", plain.Scope)
	}
	for _, field := range []string{"assetId", "connId", "sessionId", "tabId"} {
		if _, exists := plainWrapped[field]; exists {
			t.Fatalf("legacy all-null field %s present: %s", field, plain.Scope)
		}
	}

	runner := agent.NewRunner(agent.Config{Store: database})
	t.Cleanup(func() { _ = runner.Close() })
	chatConversation, err := runner.CreateConversation(ctx, "chat", map[string]any{"sessionId": "ssh-2", "assetId": "asset-9"})
	if err != nil {
		t.Fatal(err)
	}
	chatWrapped, ok := chatConversation.Scope.(map[string]any)
	if !ok {
		t.Fatalf("ai_chat scope type = %T", chatConversation.Scope)
	}
	chatInner, ok := chatWrapped["scope"].(map[string]any)
	if !ok || chatInner["sessionId"] != "ssh-2" || chatInner["assetId"] != "asset-9" {
		t.Fatalf("ai_chat scope = %+v", chatConversation.Scope)
	}
	createdInner := map[string]any{}
	if err := json.Unmarshal(wrapped["scope"], &createdInner); err != nil {
		t.Fatal(err)
	}
	if _, sameWrapper := createdInner["scope"]; sameWrapper {
		t.Fatal("ai_conversation_create scope must not double-wrap")
	}
}
