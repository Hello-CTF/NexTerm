package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	if got := decodeDesktopBinaryData(t, events[0].Data); !reflect.DeepEqual(got, []byte{0, 1, 127, 255}) {
		t.Fatalf("first binary payload = %#v", got)
	}
	if got := decodeDesktopBinaryData(t, events[1].Data); !reflect.DeepEqual(got, []byte{2, 3}) {
		t.Fatalf("second binary payload = %#v", got)
	}
	encoded, err := json.Marshal(events[2].Data)
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
	if len(events) != 3 || events[0].Name != "channel://shared-id" || events[1].Name != "channel://shared-id" || events[2].Name != "channel://other-id" {
		t.Fatalf("events after reopen = %+v", events)
	}
	closed, ok := events[0].Data.(desktopStreamClosedEvent)
	if !ok || closed.Type != desktopStreamClosedEventType {
		t.Fatalf("close payload = %#v, want one typed reopen notification", events[0].Data)
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
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam || appErr.Message != "参数错误: 通道 ID 不能为空" {
			t.Fatalf("empty channel error = %+v", err)
		}
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
	if events := window.snapshot(); len(events) != 0 {
		t.Fatalf("factory shutdown emitted reopen notifications: %+v", events)
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
		if values, ok := desktopBinaryBytes(event.Data); ok && len(values) == 1 && values[0] == 1 {
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
	if len(events) != 2 ||
		!reflect.DeepEqual(decodeDesktopBinaryData(t, events[0].Data), []byte{1}) ||
		!reflect.DeepEqual(decodeDesktopBinaryData(t, events[1].Data), []byte{2}) {
		t.Fatalf("ordered events = %+v", events)
	}
}

// gatedStreamWindow blocks every dispatch until release is closed and records
// events once they are let through.
type gatedStreamWindow struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
	unblock func()
	mu      sync.Mutex
	events  []*application.CustomEvent
}

func newGatedStreamWindow(t *testing.T) *gatedStreamWindow {
	w := &gatedStreamWindow{entered: make(chan struct{}, 64), release: make(chan struct{})}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(w.release) }) })
	w.unblock = func() { once.Do(func() { close(w.release) }) }
	return w
}

func (w *gatedStreamWindow) DispatchWailsEvent(event *application.CustomEvent) {
	w.calls.Add(1)
	w.entered <- struct{}{}
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *gatedStreamWindow) snapshot() []*application.CustomEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*application.CustomEvent(nil), w.events...)
}

func returnsWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestDesktopStreamCloseDoesNotBlockOnStuckDispatch(t *testing.T) {
	window := newGatedStreamWindow(t)
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stuck, err := factory.OpenBinary(t.Context(), ipc.ChannelRef{ID: "stuck"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := factory.OpenBinary(t.Context(), ipc.ChannelRef{ID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_ = stuck.SendBinary(context.Background(), []byte{1})
	}()
	<-window.entered

	if !returnsWithin(time.Second, func() { _ = stuck.Close() }) {
		t.Fatal("stream Close blocked behind an in-flight dispatch")
	}
	if !returnsWithin(time.Second, func() { _ = factory.Close() }) {
		t.Fatal("factory Close blocked behind an in-flight dispatch")
	}
	before := window.calls.Load()
	for _, s := range []ipc.BinaryStream{stuck, other} {
		if err := s.SendBinary(context.Background(), []byte{2}); !errors.Is(err, errDesktopStreamClosed) {
			t.Fatalf("send after shutdown = %v", err)
		}
	}
	if got := window.calls.Load(); got != before {
		t.Fatalf("dispatch calls grew from %d to %d after shutdown", before, got)
	}
	window.unblock()
	<-sendDone
}

func TestDesktopStreamCloseDuringDispatchSendsClosedEventOnceAfterData(t *testing.T) {
	window := newGatedStreamWindow(t)
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(t.Context(), ipc.ChannelRef{ID: "ordered"})
	if err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_ = stream.SendBinary(context.Background(), []byte{1})
	}()
	<-window.entered
	if !returnsWithin(time.Second, func() { _ = stream.Close(); _ = stream.Close() }) {
		t.Fatal("Close blocked behind an in-flight dispatch")
	}
	window.unblock()
	<-sendDone

	events := window.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %+v, want data then closed", events)
	}
	if _, ok := events[0].Data.(desktopBinaryEvent); !ok {
		t.Fatalf("first event = %+v, want data", events[0].Data)
	}
	if closed, ok := events[1].Data.(desktopStreamClosedEvent); !ok || closed.Type != desktopStreamClosedEventType {
		t.Fatalf("second event = %+v, want closed", events[1].Data)
	}
}

func TestDesktopStreamFactoryCloseSendsNoClosedEvent(t *testing.T) {
	window := &recordingStreamWindow{}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(t.Context(), ipc.ChannelRef{ID: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	_ = factory.Close()
	_ = stream.Close()
	if events := window.snapshot(); len(events) != 0 {
		t.Fatalf("events after factory close = %+v", events)
	}
	if _, err := factory.OpenBinary(t.Context(), ipc.ChannelRef{ID: "late"}); !errors.Is(err, errDesktopStreamClosed) {
		t.Fatalf("open after close = %v", err)
	}
}
