package production

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestVaultCredentialAndAIProfileCommands(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database, nil); err != nil {
		t.Fatal(err)
	}
	if err := registerAICommands(dispatcher, profileManager, database); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"vault_status", "vault_set_credential", "vault_list_credentials", "vault_reveal_credential", "credential_update", "ai_model_profiles", "ai_model_save", "ai_get_provider", "ai_set_provider", "ai_conversation_create", "ai_messages"} {
		if !slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("missing %s", command)
		}
	}
	for _, command := range []string{"ai_chat", "ai_cancel", "ai_get_permission", "ai_takeover_enter"} {
		if slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("unfinished AI command %s was registered", command)
		}
	}

	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_init_master", `{"password":"old-password"}`))
	response := dispatchStoreTest(dispatcher, "vault_set_credential", `{"args":{"name":"deploy key","kind":"private_key","secret":"KEY MATERIAL","source":"inline","passphrase":"secret"}}`)
	var saved map[string]string
	requireStoreTestResponse(t, response, &saved)
	response = dispatchStoreTest(dispatcher, "vault_list_credentials", `null`)
	var credentials []credentialDTO
	requireStoreTestResponse(t, response, &credentials)
	if len(credentials) != 1 || credentials[0].Source == nil || *credentials[0].Source != "inline" || !credentials[0].HasPassphrase {
		t.Fatalf("credentials = %+v", credentials)
	}
	response = dispatchStoreTest(dispatcher, "vault_reveal_credential", `{"id":"`+saved["id"]+`"}`)
	var revealed revealedCredentialDTO
	requireStoreTestResponse(t, response, &revealed)
	if revealed.Value != "KEY MATERIAL" || revealed.Passphrase == nil || *revealed.Passphrase != "secret" {
		t.Fatalf("revealed = %+v", revealed)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "credential_update", `{"args":{"id":"`+saved["id"]+`","name":"renamed","passphrase":""}}`))
	response = dispatchStoreTest(dispatcher, "vault_reveal_credential", `{"id":"`+saved["id"]+`"}`)
	requireStoreTestResponse(t, response, &revealed)
	if revealed.Value != "KEY MATERIAL" || revealed.Passphrase != nil {
		t.Fatalf("updated reveal = %+v", revealed)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_lock", `null`))
	response = dispatchStoreTest(dispatcher, "vault_list_credentials", `null`)
	requireStoreTestResponse(t, response, &credentials)
	if credentials[0].Source != nil || credentials[0].RefPath != nil || credentials[0].HasPassphrase {
		t.Fatalf("locked credentials leaked metadata = %+v", credentials[0])
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_unlock", `{"password":"old-password"}`))

	response = dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"test","baseUrl":"https://ai.example/v1","apiKey":"key","model":"model-a","temperature":0.2,"contextWindow":1000,"stream":true}}`)
	var profile profiles.Profile
	requireStoreTestResponse(t, response, &profile)
	if profile.ID == "" || profile.Name != "test" {
		t.Fatalf("saved profile = %+v", profile)
	}
	response = dispatchStoreTest(dispatcher, "ai_model_profiles", `null`)
	var overview profiles.Overview
	requireStoreTestResponse(t, response, &overview)
	if overview.ActiveID == nil || *overview.ActiveID != profile.ID || len(overview.Profiles) != 1 {
		t.Fatalf("profile overview = %+v", overview)
	}
	requireProductionNull(t, dispatchStoreTest(dispatcher, "ai_set_provider", `{"config":{"baseUrl":"https://other.example/v1","apiKey":"next","model":"model-b","temperature":0.3,"contextWindow":2000,"stream":true}}`))
	response = dispatchStoreTest(dispatcher, "ai_get_provider", `null`)
	var config map[string]any
	requireStoreTestResponse(t, response, &config)
	if config["baseUrl"] != "https://other.example/v1" || config["model"] != "model-b" {
		t.Fatalf("provider config = %+v", config)
	}
	response = dispatchStoreTest(dispatcher, "ai_presets", `null`)
	var presets []string
	requireStoreTestResponse(t, response, &presets)
	if !slices.Contains(presets, "deepseek") {
		t.Fatalf("presets = %v", presets)
	}

	response = dispatchStoreTest(dispatcher, "ai_conversation_create", `{"title":"test conversation"}`)
	var conversation conversationDTO
	requireStoreTestResponse(t, response, &conversation)
	if conversation.Title != "test conversation" || !json.Valid(conversation.Scope) {
		t.Fatalf("conversation = %+v", conversation)
	}
	if err := database.MsgInsert(ctx, conversation.ID, "user", map[string]any{"text": "hello"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	response = dispatchStoreTest(dispatcher, "ai_messages", `{"conversationId":"`+conversation.ID+`"}`)
	var messages []messageDTO
	requireStoreTestResponse(t, response, &messages)
	if len(messages) != 1 || string(messages[0].Content) != `{"text":"hello"}` {
		t.Fatalf("messages = %+v", messages)
	}
}
