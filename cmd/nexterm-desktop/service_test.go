package main

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestServiceCallPreservesClientAndDeliversEarlyChannelFrames(t *testing.T) {
	window := &recordingStreamWindow{}
	streams := newDesktopStreamFactory()
	streams.SetWindow(window)
	var lifecycle []string
	applicationCore, err := core.New(core.Config{
		Streams: streams,
		Modules: []core.Module{{
			Name: "desktop-test",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				if err := ipc.Register(dispatcher, "test_client", func(_ context.Context, call *ipc.Call, _ struct{}) (string, error) {
					return call.ClientID, nil
				}); err != nil {
					return err
				}
				return ipc.Register(dispatcher, "test_early_stream", func(ctx context.Context, call *ipc.Call, _ struct{}) (string, error) {
					stream, err := call.Streams.OpenBinary(ctx, call.Channel)
					if err != nil {
						return "", err
					}
					defer stream.Close()
					if err := stream.SendBinary(ctx, []byte{0, 127, 255}); err != nil {
						return "", err
					}
					return call.ClientID, nil
				})
			},
			Component: core.ComponentFuncs{
				StartFunc: func(context.Context) error {
					lifecycle = append(lifecycle, "start")
					return nil
				},
				ShutdownFunc: func(context.Context) error {
					lifecycle = append(lifecycle, "shutdown")
					return nil
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{app: applicationCore, streams: streams}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	response := service.Call(t.Context(), ipc.Request{Command: "test_client", ClientID: "stable-client"})
	if !response.OK || string(response.Data) != `"stable-client"` {
		t.Fatalf("client response = %+v", response)
	}
	response = service.Call(t.Context(), ipc.Request{
		Command:  "test_early_stream",
		Channel:  ipc.ChannelRef{ID: "early-channel"},
		ClientID: "stable-client",
	})
	if !response.OK || string(response.Data) != `"stable-client"` {
		t.Fatalf("early stream response = %+v", response)
	}
	events := window.snapshot()
	if len(events) != 1 || events[0].Name != "channel://early-channel" {
		t.Fatalf("early events = %+v", events)
	}
	encoded, err := json.Marshal(events[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `[0,127,255]` {
		t.Fatalf("early binary payload = %s", encoded)
	}
	response = service.Call(t.Context(), ipc.Request{Command: "test_client"})
	if !response.OK || string(response.Data) != `"desktop"` {
		t.Fatalf("default client response = %+v", response)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if len(lifecycle) != 2 || lifecycle[0] != "start" || lifecycle[1] != "shutdown" {
		t.Fatalf("lifecycle = %v", lifecycle)
	}
	if _, err := streams.OpenBinary(context.Background(), ipc.ChannelRef{ID: "after-shutdown"}); err == nil {
		t.Fatal("stream factory remained open after service shutdown")
	}
}
