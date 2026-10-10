package production

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type spawnScriptRequest struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

type spawnScriptServer struct {
	mu       sync.Mutex
	requests []spawnScriptRequest
}

func (s *spawnScriptServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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

	writer.Header().Set("Content-Type", "text/event-stream")
	switch {
	case len(parsed.Messages) == 1 && parsed.Messages[0].Role == "user" && len(parsed.Tools) == 0:
		spawnWriteContent(writer, "parent task title")
	case spawnHasUserText(&parsed, "parent task") && !spawnHasRole(&parsed, "tool"):
		spawnWriteToolCall(writer, "call-spawn-1", subagent.SpawnToolName, `{"task":"child task"}`)
	case spawnHasUserText(&parsed, "parent task"):
		if !spawnToolText(&parsed, "child done: server") {
			http.Error(writer, "spawn result missing child output", http.StatusInternalServerError)
			return
		}
		spawnWriteContent(writer, "parent done: child done: server")
	case spawnHasUserText(&parsed, "child task") && !spawnHasRole(&parsed, "tool"):
		spawnWriteToolCall(writer, "call-nested-1", subagent.SpawnToolName, `{"task":"grandchild task"}`)
	case spawnHasUserText(&parsed, "child task") && strings.Contains(spawnLastToolText(&parsed), "permission denied: "+subagent.SpawnToolName):
		spawnWriteToolCall(writer, "call-assets-1", "list_assets", `{}`)
	case spawnHasUserText(&parsed, "child task") && strings.Contains(spawnLastToolText(&parsed), "server"):
		spawnWriteContent(writer, "child done: server")
	default:
		http.Error(writer, "unexpected scripted request", http.StatusInternalServerError)
	}
}

func (s *spawnScriptServer) snapshot() []spawnScriptRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spawnScriptRequest(nil), s.requests...)
}

func spawnHasUserText(request *spawnScriptRequest, text string) bool {
	for _, message := range request.Messages {
		if message.Role == "user" && strings.Contains(spawnMessageText(message.Content), text) {
			return true
		}
	}
	return false
}

func spawnHasRole(request *spawnScriptRequest, role string) bool {
	for _, message := range request.Messages {
		if message.Role == role {
			return true
		}
	}
	return false
}

func spawnToolText(request *spawnScriptRequest, text string) bool {
	for _, message := range request.Messages {
		if message.Role == "tool" && strings.Contains(spawnMessageText(message.Content), text) {
			return true
		}
	}
	return false
}

func spawnLastToolText(request *spawnScriptRequest) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if request.Messages[index].Role == "tool" {
			return spawnMessageText(request.Messages[index].Content)
		}
	}
	return ""
}

func spawnMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	return string(content)
}

func spawnToolNames(request *spawnScriptRequest) []string {
	names := make([]string, 0, len(request.Tools))
	for _, candidate := range request.Tools {
		names = append(names, candidate.Function.Name)
	}
	return names
}

func spawnWriteToolCall(writer http.ResponseWriter, id, name, args string) {
	spawnWriteSSE(writer,
		map[string]any{"model": "test-model", "choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": args},
			}}},
			"finish_reason": nil,
		}}},
		map[string]any{"model": "test-model", "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
		}}},
	)
}

func spawnWriteContent(writer http.ResponseWriter, content string) {
	spawnWriteSSE(writer,
		map[string]any{"model": "test-model", "choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": content},
			"finish_reason": nil,
		}}},
		map[string]any{"model": "test-model", "choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}}},
	)
}

func spawnWriteSSE(writer http.ResponseWriter, payloads ...map[string]any) {
	for _, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(writer, "data: %s\n\n", encoded)
	}
	fmt.Fprint(writer, "data: [DONE]\n\n")
}

func TestComposedAIRuntimeSpawnsScopedSubagent(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"}); err != nil {
		t.Fatal(err)
	}
	initTestVault(t, ctx, database)
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	script := &spawnScriptServer{}
	providerServer := httptest.NewServer(script)
	t.Cleanup(providerServer.Close)
	if _, err := profileManager.Save(ctx, profiles.Profile{
		BaseURL: providerServer.URL, APIKey: "test-key", Model: "test-model",
		ContextWindow: 32768, Stream: true,
	}); err != nil {
		t.Fatal(err)
	}

	sessions := session.NewManager(session.Config{})
	services := ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
	if err := composeAIRuntime(ctx, &services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})

	stream := &agent.SliceStream{}
	if _, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "parent task"}, agent.StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, closed := stream.Snapshot(); closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the composed agent run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, _ := stream.Snapshot()
	spawnDispatched := false
	for _, event := range events {
		switch event.Type {
		case "error":
			for index, request := range script.snapshot() {
				encoded, _ := json.Marshal(request)
				t.Logf("request %d: %s", index, encoded)
			}
			t.Fatalf("composed agent run failed: %s", event.Message)
		case "toolCall":
			if event.Name == subagent.SpawnToolName {
				spawnDispatched = true
			}
		case "done":
			if event.Answer != "parent done: child done: server" {
				t.Fatalf("unexpected composed answer %q", event.Answer)
			}
		}
	}
	if !spawnDispatched {
		t.Fatal("spawn tool was never dispatched through the composed agent runner")
	}

	deadline = time.Now().Add(5 * time.Second)
	for len(script.snapshot()) < 6 {
		if time.Now().After(deadline) {
			t.Fatalf("provider requests = %d, want 6 (parent x2, child x3, title x1)", len(script.snapshot()))
		}
		time.Sleep(10 * time.Millisecond)
	}
	requests := script.snapshot()
	parentSeen, childSeen, titleSeen := 0, 0, 0
	for index := range requests {
		request := &requests[index]
		names := spawnToolNames(request)
		seen := make(map[string]int, len(names))
		for _, name := range names {
			seen[name]++
		}
		if seen[subagent.SpawnToolName] > 1 {
			t.Fatalf("spawn tool registered %d times in one request: %v", seen[subagent.SpawnToolName], names)
		}
		switch {
		case len(request.Messages) == 1 && request.Messages[0].Role == "user" && len(request.Tools) == 0:
			titleSeen++
		case spawnHasUserText(request, "parent task"):
			parentSeen++
			if seen[subagent.SpawnToolName] != 1 {
				t.Fatalf("parent request %d must advertise spawn exactly once: %v", index, names)
			}
			if seen["exec_commands"] != 0 || seen["write_file"] != 0 {
				t.Fatalf("parent without a session scope must not advertise session tools: %v", names)
			}
		case spawnHasUserText(request, "child task"):
			childSeen++
			for _, message := range request.Messages {
				if strings.Contains(spawnMessageText(message.Content), "parent task") || strings.Contains(spawnMessageText(message.Content), "grandchild task") {
					t.Fatalf("subagent request leaked parent or grandchild content: %s", spawnMessageText(message.Content))
				}
			}
			if seen["shell_history"] != 1 || seen["list_assets"] != 1 || seen["todo_write"] != 1 || len(names) != 3 {
				t.Fatalf("subagent scope must advertise exactly the intersected enabled tools: %v", names)
			}
		default:
			t.Fatalf("request %d matched neither the parent nor the child script", index)
		}
	}
	if parentSeen != 2 || childSeen != 3 || titleSeen != 1 {
		t.Fatalf("scripted requests parent=%d child=%d title=%d, want 2, 3 and 1", parentSeen, childSeen, titleSeen)
	}
}
