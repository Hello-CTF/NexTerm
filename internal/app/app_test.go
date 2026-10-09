package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestApplicationCoreCommandsAndModuleComposition(t *testing.T) {
	var calls []string
	application, err := New(Config{
		Name:    "NexTerm",
		Version: "1.2.3",
		GOOS:    "test-os",
		VaultStatus: func(context.Context) (any, error) {
			return map[string]any{"unlocked": false}, nil
		},
		Modules: []Module{{
			Name: "example",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return ipc.Register(dispatcher, "example_echo", func(_ context.Context, _ *ipc.Call, input string) (string, error) {
					return input, nil
				})
			},
			Component: ComponentFuncs{
				StartFunc: func(context.Context) error {
					calls = append(calls, "start")
					return nil
				},
				ShutdownFunc: func(context.Context) error {
					calls = append(calls, "stop")
					return nil
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "start" || calls[1] != "stop" {
		t.Fatalf("component calls = %v", calls)
	}

	response := application.Dispatcher.Dispatch(context.Background(), ipc.Request{Command: "app_info"}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("app_info error = %+v", response.Error)
	}
	var info Info
	if err := json.Unmarshal(response.Data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "NexTerm" || info.Version != "1.2.3" || info.Vault == nil {
		t.Fatalf("app_info = %+v", info)
	}
	response = application.Dispatcher.Dispatch(context.Background(), ipc.Request{Command: "app_platform"}, ipc.Environment{})
	if string(response.Data) != `"test-os"` {
		t.Fatalf("app_platform = %s", response.Data)
	}
	response = application.Dispatcher.Dispatch(context.Background(), ipc.Request{Command: "example_echo", Args: json.RawMessage(`"hello"`)}, ipc.Environment{})
	if string(response.Data) != `"hello"` {
		t.Fatalf("example_echo = %s", response.Data)
	}
}
