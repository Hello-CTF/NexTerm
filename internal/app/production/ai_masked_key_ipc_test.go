package production

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func setupMaskedKeyDispatcher(t *testing.T) *ipc.Dispatcher {
	t.Helper()
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
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_init_master", `{"password":"masked-key-password"}`))
	return dispatcher
}

func newModelsServer(t *testing.T, authorization *atomic.Value, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if !strings.HasSuffix(request.URL.Path, "/models") {
			t.Errorf("path = %s", request.URL.Path)
		}
		authorization.Store(request.Header.Get("Authorization"))
		_, _ = writer.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAIModelRefreshResolvesMaskedKeyFromStoredProfile(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorization atomic.Value
	var hits atomic.Int32
	server := newModelsServer(t, &authorization, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"saved","baseUrl":"`+server.URL+`","apiKey":"stored-secret","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)
	if saved.ID == "" {
		t.Fatal("saved profile has no id")
	}

	response = dispatchStoreTest(dispatcher, "ai_model_refresh", `{"profile":{"id":"`+saved.ID+`","name":"saved","baseUrl":"`+server.URL+`","apiKey":"`+profiles.MaskedAPIKey+`","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var models provider.ModelList
	requireStoreTestResponse(t, response, &models)
	if len(models.Models) != 2 || models.Models[0] != "m1" || models.Models[1] != "m2" {
		t.Fatalf("models = %v", models.Models)
	}
	if got := authorization.Load().(string); got != "Bearer stored-secret" {
		t.Fatalf("authorization = %q, want resolved stored credential", got)
	}
}

func TestAIModelRefreshSendsExplicitKeyVerbatim(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorization atomic.Value
	var hits atomic.Int32
	server := newModelsServer(t, &authorization, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_refresh", `{"profile":{"id":"","name":"draft","baseUrl":"`+server.URL+`","apiKey":"sk-explicit-new","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var models provider.ModelList
	requireStoreTestResponse(t, response, &models)
	if len(models.Models) != 2 {
		t.Fatalf("models = %v", models.Models)
	}
	if got := authorization.Load().(string); got != "Bearer sk-explicit-new" {
		t.Fatalf("authorization = %q, want explicit key verbatim", got)
	}
}

func TestAIModelRefreshMaskedKeyUnknownProfileFails(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorization atomic.Value
	var hits atomic.Int32
	server := newModelsServer(t, &authorization, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_refresh", `{"profile":{"id":"missing-profile","name":"ghost","baseUrl":"`+server.URL+`","apiKey":"`+profiles.MaskedAPIKey+`","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	if response.OK {
		t.Fatalf("refresh with unknown masked profile succeeded: %s", response.Data)
	}
	if response.Error == nil || !strings.Contains(response.Error.Message, "档案不存在") {
		t.Fatalf("error = %+v, want profile not found", response.Error)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider was contacted %d times", hits.Load())
	}
}

func TestAIModelRefreshMaskedKeyVaultLockedFails(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorization atomic.Value
	var hits atomic.Int32
	server := newModelsServer(t, &authorization, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"saved","baseUrl":"`+server.URL+`","apiKey":"stored-secret","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_lock", `null`))

	response = dispatchStoreTest(dispatcher, "ai_model_refresh", `{"profile":{"id":"`+saved.ID+`","name":"saved","baseUrl":"`+server.URL+`","apiKey":"`+profiles.MaskedAPIKey+`","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	if response.OK {
		t.Fatalf("refresh with locked vault succeeded: %s", response.Data)
	}
	if response.Error == nil || response.Error.Code != ipc.CodeVaultLocked {
		t.Fatalf("error = %+v, want %s", response.Error, ipc.CodeVaultLocked)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider was contacted %d times", hits.Load())
	}
}

func TestAIGetProviderMasksAPIKey(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)

	response := dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"saved","baseUrl":"https://ai.example/v1","apiKey":"stored-secret","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)

	response = dispatchStoreTest(dispatcher, "ai_get_provider", `null`)
	var config map[string]any
	requireStoreTestResponse(t, response, &config)
	if config["apiKey"] != profiles.MaskedAPIKey {
		t.Fatalf("apiKey = %v, want masked", config["apiKey"])
	}
	if config["baseUrl"] != "https://ai.example/v1" || config["model"] != "m1" {
		t.Fatalf("config = %+v", config)
	}
}

func TestAIModelRefreshWhitespacePaddedMaskedKey(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorization atomic.Value
	var hits atomic.Int32
	server := newModelsServer(t, &authorization, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"saved","baseUrl":"`+server.URL+`","apiKey":"stored-secret","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)

	paddedMask := "  " + profiles.MaskedAPIKey + "  "
	refreshArgs := func(id string) string {
		return `{"profile":{"id":"` + id + `","name":"saved","baseUrl":"` + server.URL + `","apiKey":"` + paddedMask + `","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`
	}

	response = dispatchStoreTest(dispatcher, "ai_model_refresh", refreshArgs(saved.ID))
	var models provider.ModelList
	requireStoreTestResponse(t, response, &models)
	if len(models.Models) != 2 || models.Models[0] != "m1" || models.Models[1] != "m2" {
		t.Fatalf("models = %v", models.Models)
	}
	if got := authorization.Load().(string); got != "Bearer stored-secret" {
		t.Fatalf("authorization = %q, want resolved stored credential, never the mask", got)
	}

	response = dispatchStoreTest(dispatcher, "ai_model_refresh", refreshArgs("missing-profile"))
	if response.OK {
		t.Fatalf("refresh with unknown profile succeeded: %s", response.Data)
	}
	if response.Error == nil || !strings.Contains(response.Error.Message, "档案不存在") {
		t.Fatalf("error = %+v, want profile not found", response.Error)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, want only the resolved refresh", hits.Load())
	}

	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_lock", `null`))
	response = dispatchStoreTest(dispatcher, "ai_model_refresh", refreshArgs(saved.ID))
	if response.OK {
		t.Fatalf("refresh with locked vault succeeded: %s", response.Data)
	}
	if response.Error == nil || response.Error.Code != ipc.CodeVaultLocked {
		t.Fatalf("error = %+v, want %s", response.Error, ipc.CodeVaultLocked)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d, locked refresh must fail before networking", hits.Load())
	}
}

func newProviderTestServer(t *testing.T, authorizations *sync.Map, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		authorizations.Store(request.URL.Path, request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/models":
			_, _ = writer.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case "/chat/completions":
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
		default:
			t.Errorf("path = %s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAITestProviderResolvesEncryptedSavedProfileKey(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorizations sync.Map
	var hits atomic.Int32
	server := newProviderTestServer(t, &authorizations, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"saved","baseUrl":"`+server.URL+`","apiKey":"stored-secret","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)

	response = dispatchStoreTest(dispatcher, "ai_test_provider", `{"id":"`+saved.ID+`"}`)
	var result provider.TestResult
	requireStoreTestResponse(t, response, &result)
	if !result.ModelsOK || !result.ChatOK {
		t.Fatalf("test result = %+v", result)
	}
	for _, path := range []string{"/models", "/chat/completions"} {
		value, ok := authorizations.Load(path)
		if !ok || value.(string) != "Bearer stored-secret" {
			t.Fatalf("%s authorization = %v, want resolved stored credential", path, value)
		}
	}
}

func TestAITestProviderLockedVaultFailsClosed(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorizations sync.Map
	var hits atomic.Int32
	server := newProviderTestServer(t, &authorizations, &hits)

	response := dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"saved","baseUrl":"`+server.URL+`","apiKey":"stored-secret","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`)
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)
	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_lock", `null`))

	response = dispatchStoreTest(dispatcher, "ai_test_provider", `{"id":"`+saved.ID+`"}`)
	if response.OK {
		t.Fatalf("test provider with locked vault succeeded: %s", response.Data)
	}
	if response.Error == nil || response.Error.Code != ipc.CodeVaultLocked {
		t.Fatalf("error = %+v, want %s", response.Error, ipc.CodeVaultLocked)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider was contacted %d times, locked test must fail before networking", hits.Load())
	}
}
