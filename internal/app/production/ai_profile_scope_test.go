package production

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func dispatchWithOwner(dispatcher *ipc.Dispatcher, ownerID, command, args string) ipc.Response {
	ctx := context.Background()
	if ownerID != "" {
		ctx = ipc.WithUserID(ctx, ownerID)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestAIModelRefreshMaskedKeyUserScopeIsolation(t *testing.T) {
	dispatcher := setupMaskedKeyDispatcher(t)
	var authorization atomic.Value
	var hits atomic.Int32
	server := newModelsServer(t, &authorization, &hits)
	const owner = "u-scope"

	saveArgs := func(id, key string) string {
		return `{"profile":{"id":"` + id + `","name":"saved","baseUrl":"` + server.URL + `","apiKey":"` + key + `","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`
	}
	refresh := func(ownerID, id string) ipc.Response {
		args := `{"profile":{"id":"` + id + `","name":"saved","baseUrl":"` + server.URL + `","apiKey":"` + profiles.MaskedAPIKey + `","model":"m1","temperature":0.3,"contextWindow":1000,"stream":true}}`
		return dispatchWithOwner(dispatcher, ownerID, "ai_model_refresh", args)
	}

	response := dispatchWithOwner(dispatcher, owner, "ai_model_save", saveArgs("", "user-secret"))
	var saved profiles.Profile
	requireStoreTestResponse(t, response, &saved)
	if saved.ID == "" {
		t.Fatal("saved profile has no id")
	}

	var models provider.ModelList
	response = refresh(owner, saved.ID)
	requireStoreTestResponse(t, response, &models)
	if got := authorization.Load().(string); got != "Bearer user-secret" {
		t.Fatalf("authorization = %q, want user-scoped stored key", got)
	}

	response = dispatchStoreTest(dispatcher, "ai_model_save", saveArgs(saved.ID, "global-secret"))
	var globalSaved profiles.Profile
	requireStoreTestResponse(t, response, &globalSaved)

	response = refresh(owner, saved.ID)
	requireStoreTestResponse(t, response, &models)
	if got := authorization.Load().(string); got != "Bearer user-secret" {
		t.Fatalf("authorization = %q, want user-scoped key, never the same-id global key", got)
	}

	response = refresh("", saved.ID)
	requireStoreTestResponse(t, response, &models)
	if got := authorization.Load().(string); got != "Bearer global-secret" {
		t.Fatalf("authorization = %q, want anonymous fallback to global key", got)
	}
	if hits.Load() != 3 {
		t.Fatalf("provider hits = %d, want 3", hits.Load())
	}
}
