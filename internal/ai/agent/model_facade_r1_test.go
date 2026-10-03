package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestR1ModelFacadesMatchTypeScriptContracts(t *testing.T) {
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
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_presets"}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("presets failed: %+v", response.Error)
	}
	var presetNames []string
	if err := json.Unmarshal(response.Data, &presetNames); err != nil || len(presetNames) == 0 {
		t.Fatalf("preset names = %s, err = %v", response.Data, err)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_model_save", Args: json.RawMessage(`{"profile":{"name":"draft","baseUrl":"http://127.0.0.1:1/v1","model":"primary","contextWindow":32000}}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("save failed: %+v", response.Error)
	}
	var saved profiles.Profile
	if err := json.Unmarshal(response.Data, &saved); err != nil || saved.ID == "" || saved.Name != "draft" {
		t.Fatalf("saved profile = %s, err = %v", response.Data, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("refresh path = %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"draft-model"}]}`))
	}))
	defer server.Close()
	payload, _ := json.Marshal(map[string]any{"profile": map[string]any{"name": "draft", "baseUrl": server.URL + "/v1", "apiKey": "draft-key", "model": "draft", "contextWindow": 32000}})
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_model_refresh", Args: payload}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("refresh failed: %+v", response.Error)
	}
	var models []string
	if err := json.Unmarshal(response.Data, &models); err != nil || len(models) != 1 || models[0] != "draft-model" {
		t.Fatalf("refresh models = %s, err = %v", response.Data, err)
	}
}
