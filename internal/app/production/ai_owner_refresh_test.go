package production

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func newAIOwnerModelsServer(t *testing.T, authorization *atomic.Value, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		authorization.Store(request.Header.Get("Authorization"))
		_, _ = writer.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAIModelRefreshOwnerBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		shared  bool
		open    bool
		ownerID string
		allowed bool
	}{
		{name: "gateway without identity is rejected", shared: false, allowed: false},
		{name: "desktop shared state remains available", shared: true, allowed: true},
		{name: "explicit open remains available", shared: false, open: true, allowed: true},
		{name: "account owner remains available", shared: false, ownerID: "owner-a", allowed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := store.OpenInMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			manager, err := profiles.NewManager(ctx, database, profiles.WithShared(test.shared))
			if err != nil {
				t.Fatal(err)
			}
			dispatcher := ipc.NewDispatcher()
			if err := registerAICommands(dispatcher, manager, database); err != nil {
				t.Fatal(err)
			}
			var authorization atomic.Value
			var hits atomic.Int32
			modelServer := newAIOwnerModelsServer(t, &authorization, &hits)
			legacy, err := json.Marshal(map[string]any{
				"version": 1,
				"profiles": []map[string]any{{
					"id": "legacy", "name": "legacy", "baseUrl": modelServer.URL,
					"apiKey": "legacy-secret", "model": "m1", "contextWindow": 1000, "stream": false,
				}},
				"activeId": "legacy",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.SettingSet(ctx, store.AIProfilesSettingKey, string(legacy)); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"profile": map[string]any{
				"id": "draft", "name": "draft", "baseUrl": modelServer.URL,
				"apiKey": "explicit-secret", "model": "m1", "contextWindow": 1000, "stream": false,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if test.open {
				ctx = owner.WithOpenAccess(ctx)
			}
			if test.ownerID != "" {
				ctx = ipc.WithUserID(ctx, test.ownerID)
			}
			response := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_model_refresh", Args: body}, ipc.Environment{})
			if !test.allowed {
				if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
					t.Fatalf("gateway refresh response = %+v, want forbidden", response)
				}
				if hits.Load() != 0 {
					t.Fatalf("gateway plaintext refresh made %d provider requests", hits.Load())
				}
				return
			}
			var models provider.ModelList
			if !response.OK {
				t.Fatalf("refresh failed: %+v", response.Error)
			}
			if err := json.Unmarshal(response.Data, &models); err != nil {
				t.Fatal(err)
			}
			if len(models.Models) != 2 || hits.Load() != 1 {
				t.Fatalf("models = %+v, provider hits = %d", models.Models, hits.Load())
			}
			if got := authorization.Load().(string); got != "Bearer explicit-secret" {
				t.Fatalf("authorization = %q, want explicit input key", got)
			}
		})
	}
}
