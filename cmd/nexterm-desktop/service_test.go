package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

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
	if len(events) != 2 || events[0].Name != "channel://early-channel" || events[1].Name != "channel://early-channel" {
		t.Fatalf("early events = %+v", events)
	}
	encoded, err := json.Marshal(events[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeDesktopBinaryData(t, events[0].Data); string(got) != "\x00\x7f\xff" {
		t.Fatalf("early binary payload = %s", encoded)
	}
	closed, ok := events[1].Data.(desktopStreamClosedEvent)
	if !ok || closed.Type != desktopStreamClosedEventType {
		t.Fatalf("early close payload = %#v, want typed close after binary", events[1].Data)
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

func TestServiceShutdownFailsFastWhenStreamDispatchIsStuck(t *testing.T) {
	window := newGatedStreamWindow(t)
	streams := newDesktopStreamFactory()
	streams.SetWindow(window)
	stuck, err := streams.OpenBinary(t.Context(), ipc.ChannelRef{ID: "stuck"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := streams.OpenBinary(t.Context(), ipc.ChannelRef{ID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct{ closeReturned, sendErr error }
	componentDone := make(chan outcome, 1)
	applicationCore, err := core.New(core.Config{
		Streams: streams,
		Modules: []core.Module{{
			Name: "desktop-shutdown-test",
			Component: core.ComponentFuncs{
				ShutdownFunc: func(context.Context) error {
					closeErr := stuck.Close()
					componentDone <- outcome{closeReturned: closeErr, sendErr: other.SendBinary(context.Background(), []byte{2})}
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
	go func() { _ = stuck.SendBinary(context.Background(), []byte{1}) }()
	<-window.entered

	result := make(chan error, 1)
	go func() { result <- service.ServiceShutdown() }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServiceShutdown did not return promptly")
	}
	select {
	case got := <-componentDone:
		if got.closeReturned != nil || !errors.Is(got.sendErr, errDesktopStreamClosed) {
			t.Fatalf("component saw close=%v send=%v", got.closeReturned, got.sendErr)
		}
	default:
		t.Fatal("component shutdown did not run")
	}
	if got := window.calls.Load(); got != 1 {
		t.Fatalf("dispatch calls = %d, want only the stuck one", got)
	}
}

func TestServiceShutdownReleasesSenderStuckInDispatchBeforeClose(t *testing.T) {
	window := newGatedStreamWindow(t)
	streams := newDesktopStreamFactory()
	streams.SetWindow(window)
	stuck, err := streams.OpenBinary(t.Context(), ipc.ChannelRef{ID: "stuck"})
	if err != nil {
		t.Fatal(err)
	}
	senderErr := make(chan error, 1)
	var senders sync.WaitGroup
	applicationCore, err := core.New(core.Config{
		Streams: streams,
		Modules: []core.Module{{
			Name: "desktop-wait-sender-test",
			Component: core.ComponentFuncs{
				ShutdownFunc: func(context.Context) error {
					senders.Wait()
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
	senders.Add(1)
	go func() {
		defer senders.Done()
		senderErr <- stuck.SendBinary(context.Background(), []byte{1})
	}()
	<-window.entered

	result := make(chan error, 1)
	go func() { result <- service.ServiceShutdown() }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServiceShutdown waited on a sender stuck in dispatch")
	}
	if err := <-senderErr; !errors.Is(err, errDesktopStreamClosed) {
		t.Fatalf("sender err = %v, want errDesktopStreamClosed", err)
	}
}
