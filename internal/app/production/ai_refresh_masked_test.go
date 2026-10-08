package production

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestAIModelRefreshRejectsResidualMaskedKeys(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "refresh-test-password"); err != nil {
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
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(server.Close)

	overview, err := profileManager.Save(ctx, profiles.Profile{BaseURL: server.URL, APIKey: "real-key", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	profileID := overview.Profiles[0].ID

	stampMasked := func(t *testing.T, envelope bool) {
		t.Helper()
		raw, found, err := database.SettingGet(ctx, profiles.SettingKey)
		if err != nil || !found {
			t.Fatalf("setting: found=%v err=%v", found, err)
		}
		key := profiles.MaskedAPIKey
		if envelope {
			protector := database.SecretProtector()
			if protector == nil {
				t.Fatal("secret protector unavailable")
			}
			key, err = protector.EncryptSecret(ctx, profiles.MaskedAPIKey)
			if err != nil {
				t.Fatal(err)
			}
		}
		var state struct {
			Profiles []map[string]any `json:"profiles"`
		}
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		for _, profile := range state.Profiles {
			if profile["id"] == profileID {
				profile["apiKey"] = key
			}
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.SettingSet(ctx, profiles.SettingKey, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}

	refresh := func(t *testing.T) ipc.Response {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"profile": map[string]any{
				"id": profileID, "baseUrl": server.URL, "apiKey": profiles.MaskedAPIKey,
				"model": "m", "temperature": 0.3, "contextWindow": 1000, "stream": false,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_model_refresh", Args: body}, ipc.Environment{})
	}

	for _, envelope := range []bool{false, true} {
		stampMasked(t, envelope)
		response := refresh(t)
		if response.OK {
			t.Fatalf("envelope=%v: refresh with residual masked key succeeded", envelope)
		}
		if response.Error == nil || !strings.Contains(response.Error.Message, "API Key") {
			t.Fatalf("envelope=%v: error = %+v", envelope, response.Error)
		}
		if hits.Load() != 0 {
			t.Fatalf("envelope=%v: provider received %d requests", envelope, hits.Load())
		}
	}
}
