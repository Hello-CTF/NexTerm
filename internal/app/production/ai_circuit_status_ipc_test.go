package production

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestAICircuitStatusCommandRegistrationAndInvocation(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommands(dispatcher, manager, database); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dispatcher.Commands(), "ai_circuit_status") {
		t.Fatalf("missing ai_circuit_status in %v", dispatcher.Commands())
	}

	response := dispatchStoreTest(dispatcher, "ai_circuit_status", `{"id":""}`)
	if response.OK || !strings.Contains(response.Error.Message, "no active AI model profile") {
		t.Fatalf("empty id without active profile = %+v", response)
	}
	response = dispatchStoreTest(dispatcher, "ai_circuit_status", `{"id":"missing"}`)
	if response.OK || !strings.Contains(response.Error.Message, profiles.ErrProfileNotFound.Error()) {
		t.Fatalf("unknown profile = %+v", response)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":{"message":"injected"}}`))
	}))
	defer server.Close()

	response = dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"breaker","baseUrl":"`+server.URL+`","apiKey":"key","model":"m","temperature":0.2,"contextWindow":1000,"stream":true,"circuitFailureThreshold":1,"circuitCooldownSeconds":3600}}`)
	var openProfile profiles.Profile
	requireStoreTestResponse(t, response, &openProfile)
	response = dispatchStoreTest(dispatcher, "ai_model_save", `{"profile":{"name":"counting","baseUrl":"`+server.URL+`","apiKey":"key","model":"m","temperature":0.2,"contextWindow":1000,"stream":true,"circuitFailureThreshold":5}}`)
	var countingProfile profiles.Profile
	requireStoreTestResponse(t, response, &countingProfile)

	response = dispatchStoreTest(dispatcher, "ai_circuit_status", `{"id":"`+openProfile.ID+`"}`)
	if string(response.Data) != `{"consecutiveFailures":0,"openUntil":null}` {
		t.Fatalf("zero state wire = %s", response.Data)
	}
	var status aiCircuitStatusDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "ai_circuit_status", `null`), &status)
	if status.ConsecutiveFailures != 0 || status.OpenUntil != nil {
		t.Fatalf("active profile zero state = %+v", status)
	}

	client, err := manager.ClientFor(openProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatBlock(ctx, provider.ChatRequest{}); err == nil {
		t.Fatal("failing server succeeded")
	}
	response = dispatchStoreTest(dispatcher, "ai_circuit_status", `{"id":"`+openProfile.ID+`"}`)
	requireStoreTestResponse(t, response, &status)
	if status.ConsecutiveFailures != 1 || status.OpenUntil == nil {
		t.Fatalf("open state = %+v", status)
	}
	if until := time.UnixMilli(*status.OpenUntil); !until.After(time.Now()) {
		t.Fatalf("openUntil = %v, want future", until)
	}
	var wire map[string]any
	if err := json.Unmarshal(response.Data, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["openUntil"].(float64); !ok {
		t.Fatalf("openUntil wire type = %T", wire["openUntil"])
	}

	counting, err := manager.ClientFor(countingProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := counting.ChatBlock(ctx, provider.ChatRequest{}); err == nil {
		t.Fatal("failing server succeeded")
	}
	response = dispatchStoreTest(dispatcher, "ai_circuit_status", `{"id":"`+countingProfile.ID+`"}`)
	requireStoreTestResponse(t, response, &status)
	if status.ConsecutiveFailures != 3 || status.OpenUntil != nil {
		t.Fatalf("counting but closed state = %+v", status)
	}
}
