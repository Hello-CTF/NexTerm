package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recordingJSONStream struct {
	mu     sync.Mutex
	events []map[string]any
	closed chan struct{}
	once   sync.Once
}

func newRecordingJSONStream() *recordingJSONStream {
	return &recordingJSONStream{closed: make(chan struct{})}
}

func (s *recordingJSONStream) SendJSON(_ context.Context, raw json.RawMessage) error {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closed:
		return errors.New("closed")
	default:
	}
	s.events = append(s.events, event)
	return nil
}

func (s *recordingJSONStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestChatIPCUsesSharedDispatcherAndChannel(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	chat := sequenceModel(schema.AssistantMessage("ok", nil))
	runner := NewRunner(Config{Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil }, Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage})
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	runtime := []string{"ai_chat", "ai_cancel", "ai_steer", "ai_confirm", "ai_answer", "ai_hitl_snapshot", "ai_hitl_events", "ai_get_permission", "ai_set_permission"}
	registered := dispatcher.Commands()
	if len(registered) != len(runtime) {
		t.Fatalf("agent module must register exactly the runtime surface, got %v", registered)
	}
	for _, command := range runtime {
		found := false
		for _, name := range registered {
			found = found || name == command
		}
		if !found {
			t.Errorf("missing command %s", command)
		}
	}
	stream := newRecordingJSONStream()
	channels := make(chan string, 1)
	factory := ipc.StreamFactoryFuncs{JSON: func(_ context.Context, channel ipc.ChannelRef) (ipc.JSONStream, error) {
		channels <- channel.ID
		return stream, nil
	}}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_chat", Args: json.RawMessage(`{"args":{"message":"hello"}}`), Channel: ipc.ChannelRef{ID: "channel-1"}}, ipc.Environment{Streams: factory})
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	var started StartResponse
	if err := json.Unmarshal(response.Data, &started); err != nil || started.JobID == "" || started.ConversationID == "" {
		t.Fatalf("start response=%s err=%v", response.Data, err)
	}
	if channel := <-channels; channel != "channel-1" {
		t.Fatalf("channel=%q", channel)
	}
	select {
	case <-stream.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("IPC stream did not close")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	terminals := 0
	for _, event := range stream.events {
		if event["type"] == "done" || event["type"] == "error" {
			terminals++
		}
	}
	if terminals != 1 || stream.events[len(stream.events)-1]["type"] != "done" {
		t.Fatalf("events=%+v", stream.events)
	}
}

func TestSteerDispatchParsesWireArgs(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	runner := NewRunner(Config{Store: storage})
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	unknown := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_steer", Args: json.RawMessage(`{"jobId":"job-missing","message":"hi"}`)}, ipc.Environment{})
	if unknown.OK || unknown.Error == nil || !strings.Contains(unknown.Error.Message, ErrJobNotFound.Error()) {
		t.Fatalf("unknown job dispatch = %+v", unknown)
	}
	empty := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_steer", Args: json.RawMessage(`{"jobId":"job-missing","message":"  "}`)}, ipc.Environment{})
	if empty.OK || empty.Error == nil || !strings.Contains(empty.Error.Message, "补充指令不能为空") {
		t.Fatalf("empty message dispatch = %+v", empty)
	}
}

func TestModuleComposesAfterOwnerModules(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	runner := NewRunner(Config{Store: storage})
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	ownerCommands := []string{
		"ai_conversation_create", "ai_conversation_list", "ai_conversation_delete", "ai_messages",
		"ai_presets", "ai_models", "ai_model_refresh", "ai_model_profiles", "ai_model_save",
		"ai_model_delete", "ai_model_activate", "ai_model_preset",
		"ai_test_provider", "ai_get_provider", "ai_set_provider",
	}
	for _, command := range ownerCommands {
		if err := dispatcher.RegisterRaw(command, func(context.Context, *ipc.Call) (any, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := Module(runner).RegisterCommands(dispatcher); err != nil {
		t.Fatalf("agent module must compose after owner modules: %v", err)
	}
	for _, command := range ownerCommands {
		for _, name := range dispatcher.Commands() {
			if name != command {
				continue
			}
			response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: command}, ipc.Environment{})
			if !response.OK {
				t.Fatalf("owner command %s shadowed: %+v", command, response.Error)
			}
		}
	}
}
