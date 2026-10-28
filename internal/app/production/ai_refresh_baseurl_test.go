package production

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// A masked key resolves to the stored secret only for the profile's own baseUrl.
func TestAIModelRefreshMaskedKeyBoundToStoredBaseURL(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := vault.Load(ctx, database).InitMaster(ctx, "refresh-test-password"); err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, profileManager, database); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	seen := map[string][]string{}
	handler := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[name] = append(seen[name], r.Header.Get("Authorization")+"|"+r.Header.Get("x-api-key"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}
	trusted := httptest.NewServer(handler("trusted"))
	t.Cleanup(trusted.Close)
	attacker := httptest.NewServer(handler("attacker"))
	t.Cleanup(attacker.Close)

	overview, err := profileManager.Save(ctx, profiles.Profile{BaseURL: trusted.URL, APIKey: "real-secret-key", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	profileID := overview.Profiles[0].ID

	refresh := func(baseURL string) ipc.Response {
		body, err := json.Marshal(map[string]any{"profile": map[string]any{
			"id": profileID, "baseUrl": baseURL, "apiKey": profiles.MaskedAPIKey,
			"model": "m", "temperature": 0.3, "contextWindow": 1000, "stream": false,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_model_refresh", Args: body}, ipc.Environment{})
	}

	response := refresh(attacker.URL)
	if response.OK {
		t.Fatal("refresh with masked key and a different baseUrl succeeded")
	}
	if response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("error = %+v, want bad-param", response.Error)
	}
	mu.Lock()
	if len(seen["attacker"]) != 0 {
		t.Fatalf("attacker endpoint received requests: %v", seen["attacker"])
	}
	mu.Unlock()

	if response := refresh(trusted.URL + "/"); !response.OK {
		t.Fatalf("refresh with the stored baseUrl failed: %+v", response.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen["trusted"]) == 0 {
		t.Fatal("trusted endpoint was not contacted")
	}
}
