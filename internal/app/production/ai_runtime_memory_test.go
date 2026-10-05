package production

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/memory"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type memoryScriptServer struct {
	mu       sync.Mutex
	requests []spawnScriptRequest
}

func (s *memoryScriptServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var parsed spawnScriptRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, parsed)
	s.mu.Unlock()
	spawnWriteContent(writer, "done")
}

func (s *memoryScriptServer) snapshot() []spawnScriptRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spawnScriptRequest(nil), s.requests...)
}

func (s *memoryScriptServer) toolNames(index int) []string {
	request := s.snapshot()[index]
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

func composeMemoryRuntime(t *testing.T) (*ProductionServices, *store.Store, *memoryScriptServer, string, string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	script := &memoryScriptServer{}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)
	if _, err := profileManager.Save(ctx, profiles.Profile{
		BaseURL: providerServer.URL, APIKey: "test-key", Model: "test-model",
		Temperature: 0, ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &outcomeFakeTransport{}, nil
		}),
	})
	connected, err := sessions.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, dataDir: dataDir}
	if err := composeAIRuntime(ctx, services, "test-client"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})
	return services, database, script, connected.ID, dataDir
}

func composedMemoryDispatch(t *testing.T, services *ProductionServices, command, args string) ipc.Response {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := agent.Module(services.Agent).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestComposedMemoryStoreLivesAtPlatformDataPath(t *testing.T) {
	services, _, _, _, dataDir := composeMemoryRuntime(t)
	path := filepath.Join(dataDir, "memory.db")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("memory.db missing at the platform data path: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("memory.db permissions = %o, want 600", perm)
	}

	scope := `{"tenant":"local","subject":"default"}`
	created := composedMemoryDispatch(t, services, "memory_create", `{"scope":`+scope+`,"topic":"operations","content":"restart at 02:00"}`)
	if !created.OK {
		t.Fatalf("memory_create = %+v", created.Error)
	}
	settings := composedMemoryDispatch(t, services, "memory_settings_get", `{"scope":`+scope+`}`)
	if !settings.OK || !strings.Contains(string(settings.Data), `"injectionEnabled":false`) {
		t.Fatalf("memory_settings_get = %s err=%+v", settings.Data, settings.Error)
	}

	services.closeAIRuntime()
	reopened, err := memory.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	defer reopened.Close()
	entries, err := reopened.Index(context.Background(), productionMemoryScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Topic != "operations" {
		t.Fatalf("reopened index = %+v", entries)
	}
}

func TestComposedMemoryInjectionIsOptIn(t *testing.T) {
	ctx := context.Background()
	services, database, script, sessionID, dataDir := composeMemoryRuntime(t)

	run := func(message string) string {
		t.Helper()
		stream := &agent.SliceStream{}
		response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: message, Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range waitOutcomeClosed(t, stream) {
			if event.Type == "error" {
				t.Fatalf("composed agent run failed: %s", event.Message)
			}
		}
		return response.ConversationID
	}

	run("first question")
	requests := script.snapshot()
	if len(requests) == 0 {
		t.Fatal("provider saw no request")
	}
	if strings.Contains(string(mustJSON(t, requests[0])), "Long-term operational memory") {
		t.Fatal("default settings injected memory into the provider request")
	}
	for _, name := range script.toolNames(0) {
		if strings.HasPrefix(name, "memory_") {
			t.Fatalf("default settings advertised memory tool %s", name)
		}
	}

	memoryStore, err := memory.Open(ctx, filepath.Join(dataDir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Create(ctx, productionMemoryScope, memory.CreateInput{Topic: "operations", Content: "restart at 02:00"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := memoryStore.UpdateSettings(ctx, productionMemoryScope, memory.SettingsInput{InjectionEnabled: &enabled, ToolsEnabled: &enabled}, 0); err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.Close(); err != nil {
		t.Fatal(err)
	}

	conversationID := run("second question")
	requests = script.snapshot()
	latest := -1
	for index, request := range requests {
		for _, message := range request.Messages {
			if message.Role == "user" && spawnMessageText(message.Content) == "second question" {
				latest = index
			}
		}
	}
	if latest < 0 {
		t.Fatal("second chat request missing from the provider requests")
	}
	payload := string(mustJSON(t, requests[latest]))
	if !strings.Contains(payload, "Long-term operational memory") || !strings.Contains(payload, "restart at 02:00") {
		t.Fatalf("opted-in injection missing from the provider request: %s", payload)
	}
	found := map[string]bool{}
	for _, name := range script.toolNames(latest) {
		found[name] = true
	}
	for _, name := range []string{"memory_save", "memory_list", "memory_recall", "memory_forget"} {
		if !found[name] {
			t.Fatalf("opted-in run missing tool %s from %v", name, found)
		}
	}

	rows, err := database.MsgList(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("persisted rows = %d, want user+assistant only", len(rows))
	}
	for _, row := range rows {
		if strings.Contains(row.ContentJSON, "restart at 02:00") || strings.Contains(row.ContentJSON, "Long-term operational memory") {
			t.Fatalf("memory polluted the persisted conversation: %s", row.ContentJSON)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
