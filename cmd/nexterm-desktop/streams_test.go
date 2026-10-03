package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type recordingStreamWindow struct {
	mu     sync.Mutex
	events []*application.CustomEvent
	before func(*application.CustomEvent)
}

func (w *recordingStreamWindow) DispatchWailsEvent(event *application.CustomEvent) {
	if w.before != nil {
		w.before(event)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *recordingStreamWindow) snapshot() []*application.CustomEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*application.CustomEvent(nil), w.events...)
}

func TestDesktopStreamsPreserveBinaryJSONAndChannelOrder(t *testing.T) {
	window := &recordingStreamWindow{}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	binary, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "pty-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range [][]byte{{0, 1, 127, 255}, {2, 3}} {
		if err := binary.SendBinary(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		frame[0] = 99
	}
	jsonStream, err := factory.OpenJSON(context.Background(), ipc.ChannelRef{ID: "ai-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonStream.SendJSON(context.Background(), json.RawMessage(`{"type":"delta","sequence":9007199254740993}`)); err != nil {
		t.Fatal(err)
	}

	events := window.snapshot()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Name != "channel://pty-1" || events[1].Name != "channel://pty-1" || events[2].Name != "channel://ai-1" {
		t.Fatalf("event topics = %q, %q, %q", events[0].Name, events[1].Name, events[2].Name)
	}
	if got := events[0].Data; !reflect.DeepEqual(got, []int{0, 1, 127, 255}) {
		t.Fatalf("first binary payload = %#v", got)
	}
	if got := events[1].Data; !reflect.DeepEqual(got, []int{2, 3}) {
		t.Fatalf("second binary payload = %#v", got)
	}
	encoded, err := json.Marshal(events[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `[0,1,127,255]` {
		t.Fatalf("binary JSON = %s", encoded)
	}
	encoded, err = json.Marshal(events[2].Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"sequence":9007199254740993,"type":"delta"}` {
		t.Fatalf("AI JSON = %s", encoded)
	}
}

func TestDesktopStreamCloseReopenAndIsolation(t *testing.T) {
	window := &recordingStreamWindow{}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	first, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "shared-id"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.OpenJSON(context.Background(), ipc.ChannelRef{ID: "shared-id"}); err == nil {
		t.Fatal("expected duplicate producer rejection")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.SendBinary(context.Background(), []byte{1}); !errors.Is(err, errDesktopStreamClosed) {
		t.Fatalf("closed send error = %v", err)
	}
	second, err := factory.OpenJSON(context.Background(), ipc.ChannelRef{ID: "shared-id"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "other-id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.SendJSON(context.Background(), json.RawMessage(`{"owner":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if err := other.SendBinary(context.Background(), []byte{7}); err != nil {
		t.Fatal(err)
	}
	if err := first.SendBinary(context.Background(), []byte{8}); !errors.Is(err, errDesktopStreamClosed) {
		t.Fatalf("stale producer error = %v", err)
	}
	events := window.snapshot()
	if len(events) != 2 || events[0].Name != "channel://shared-id" || events[1].Name != "channel://other-id" {
		t.Fatalf("events after reopen = %+v", events)
	}
}

func TestDesktopStreamRejectsUnavailableInvalidAndCanceledCalls(t *testing.T) {
	factory := newDesktopStreamFactory()
	if _, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "pty"}); !errors.Is(err, ipc.ErrStreamsUnavailable) {
		t.Fatalf("missing window error = %v", err)
	}
	window := &recordingStreamWindow{}
	factory.SetWindow(window)
	if _, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{}); err == nil {
		t.Fatal("expected empty channel rejection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := factory.OpenBinary(ctx, ipc.ChannelRef{ID: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open error = %v", err)
	}
	stream, err := factory.OpenJSON(context.Background(), ipc.ChannelRef{ID: "json"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{`, `{} {}`} {
		if err := stream.SendJSON(context.Background(), json.RawMessage(input)); err == nil {
			t.Fatalf("expected invalid JSON rejection for %q", input)
		}
	}
	if err := stream.SendJSON(ctx, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled send error = %v", err)
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.SendJSON(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, errDesktopStreamClosed) {
		t.Fatalf("factory close send error = %v", err)
	}
	if _, err := factory.OpenJSON(context.Background(), ipc.ChannelRef{ID: "after-close"}); !errors.Is(err, errDesktopStreamClosed) {
		t.Fatalf("factory reopen error = %v", err)
	}
}

func TestDesktopStreamSerializesConcurrentSendsPerChannel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	window := &recordingStreamWindow{before: func(event *application.CustomEvent) {
		if values, ok := event.Data.([]int); ok && len(values) == 1 && values[0] == 1 {
			once.Do(func() { close(started) })
			<-release
		}
	}}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "ordered"})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- stream.SendBinary(context.Background(), []byte{1}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first send did not start")
	}
	second := make(chan error, 1)
	go func() { second <- stream.SendBinary(context.Background(), []byte{2}) }()
	select {
	case err := <-second:
		t.Fatalf("second send overtook first: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for _, result := range []<-chan error{first, second} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("send did not finish")
		}
	}
	events := window.snapshot()
	if len(events) != 2 || !reflect.DeepEqual(events[0].Data, []int{1}) || !reflect.DeepEqual(events[1].Data, []int{2}) {
		t.Fatalf("ordered events = %+v", events)
	}
}
