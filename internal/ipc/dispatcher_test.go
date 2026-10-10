package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type echoInput struct {
	Name      string `json:"name"`
	WithCreds bool   `json:"withCreds"`
}

func TestDispatcherSupportsFlatAndNestedArguments(t *testing.T) {
	dispatcher := NewDispatcher()
	handler := func(_ context.Context, call *Call, input echoInput) (echoInput, error) {
		if call.ClientID != "client-1" {
			t.Fatalf("ClientID = %q", call.ClientID)
		}
		return input, nil
	}
	if err := Register(dispatcher, "echo", handler); err != nil {
		t.Fatal(err)
	}
	if err := RegisterNested(dispatcher, "nested", handler); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		command string
		args    string
	}{
		{name: "flat", command: "echo", args: `{"name":"box","withCreds":true}`},
		{name: "nested", command: "nested", args: `{"args":{"name":"box","withCreds":true}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := dispatcher.Dispatch(context.Background(), Request{
				Command: test.command,
				Args:    json.RawMessage(test.args),
			}, Environment{ClientID: "client-1"})
			if !response.OK {
				t.Fatalf("response error: %+v", response.Error)
			}
			if got, want := string(response.Data), `{"name":"box","withCreds":true}`; got != want {
				t.Fatalf("data = %s, want %s", got, want)
			}
		})
	}
}

func TestDispatcherResponseContracts(t *testing.T) {
	dispatcher := NewDispatcher()
	if err := Register(dispatcher, "null", func(context.Context, *Call, struct{}) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Register(dispatcher, "fail", func(context.Context, *Call, struct{}) (any, error) {
		return nil, NewError(CodeHostKeyPending, "主机指纹待确认").WithDetail(map[string]any{"host": "example"})
	}); err != nil {
		t.Fatal(err)
	}

	response := dispatcher.Dispatch(context.Background(), Request{Command: "null"}, Environment{})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"ok":true,"data":null}`; got != want {
		t.Fatalf("success = %s, want %s", got, want)
	}

	response = dispatcher.Dispatch(context.Background(), Request{Command: "fail"}, Environment{})
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"ok":false,"error":{"code":"host_key_pending","message":"主机指纹待确认","detail":{"host":"example"}}}`; got != want {
		t.Fatalf("failure = %s, want %s", got, want)
	}

	response = dispatcher.Dispatch(context.Background(), Request{Command: "missing"}, Environment{})
	if response.Error == nil || response.Error.Code != CodeNotFound {
		t.Fatalf("unknown command error = %+v", response.Error)
	}
	if strings.Contains(response.Error.Message, "missing") {
		t.Fatalf("unknown command message leaks command name: %q", response.Error.Message)
	}
	if cause := response.Error.Unwrap(); cause == nil || !strings.Contains(cause.Error(), "missing") {
		t.Fatalf("unknown command cause should preserve the command name, got %v", cause)
	}
	if err := dispatcher.RegisterRaw("null", func(context.Context, *Call) (any, error) { return nil, nil }); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
	if got := dispatcher.Commands(); len(got) != 2 || got[0] != "fail" || got[1] != "null" {
		t.Fatalf("commands = %v", got)
	}
}

func TestDispatcherReturnsBadParamsAndContainsPanics(t *testing.T) {
	dispatcher := NewDispatcher()
	if err := Register(dispatcher, "echo", func(_ context.Context, _ *Call, input echoInput) (echoInput, error) {
		return input, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Register(dispatcher, "panic", func(context.Context, *Call, struct{}) (any, error) {
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}

	response := dispatcher.Dispatch(context.Background(), Request{Command: "echo", Args: json.RawMessage(`{"name":123}`)}, Environment{})
	if response.Error == nil || response.Error.Code != CodeBadParam {
		t.Fatalf("bad parameter error = %+v", response.Error)
	}
	if strings.Contains(response.Error.Message, "json:") || strings.Contains(response.Error.Message, "Go struct") {
		t.Fatalf("bad parameter message leaks decoder details: %q", response.Error.Message)
	}
	if cause := response.Error.Unwrap(); cause == nil || !strings.Contains(cause.Error(), "json:") {
		t.Fatalf("bad parameter cause should preserve the decode error, got %v", cause)
	}
	response = dispatcher.Dispatch(context.Background(), Request{Command: "panic"}, Environment{})
	if response.Error == nil || response.Error.Code != CodeInternal {
		t.Fatalf("panic error = %+v", response.Error)
	}
	if strings.Contains(response.Error.Message, "boom") || strings.Contains(response.Error.Message, "panic") {
		t.Fatalf("panic message leaks internals: %q", response.Error.Message)
	}
	if cause := response.Error.Unwrap(); cause == nil || !strings.Contains(cause.Error(), "boom") {
		t.Fatalf("panic cause should preserve the panic value, got %v", cause)
	}
}

func TestNormalizeErrorPreservesStructuredErrors(t *testing.T) {
	original := NewError(CodeTimeout, "超时")
	if got := NormalizeError(original); got != original {
		t.Fatalf("NormalizeError returned %+v", got)
	}
	if got := NormalizeError(errors.New("plain")); got.Code != CodeInternal {
		t.Fatalf("plain error code = %s", got.Code)
	}
}

func TestDispatcherEmitsSyncStatusOnlyForRelevantSuccessfulWrites(t *testing.T) {
	dispatcher := NewDispatcher()
	var events []Event
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	})
	for _, command := range []string{"asset_create", "asset_update", "asset_delete", "ai_set_permission", "sync_apply_objects"} {
		command := command
		if err := Register(dispatcher, command, func(_ context.Context, _ *Call, _ struct{}) (any, error) {
			if command == "asset_update" {
				return nil, errors.New("stop")
			}
			return struct{}{}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"asset_create", "asset_update", "asset_delete", "ai_set_permission", "sync_apply_objects"} {
		_ = dispatcher.Dispatch(context.Background(), Request{Command: command}, Environment{Events: emitter})
	}
	if len(events) != 3 || events[0].Event != TopicSyncStatus || events[1].Event != TopicSyncStatus || events[2].Event != TopicSyncStatus {
		t.Fatalf("events = %+v, want three sync status events", events)
	}
}
