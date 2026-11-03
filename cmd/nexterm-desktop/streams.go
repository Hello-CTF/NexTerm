package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/diagcapture"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var errDesktopStreamClosed = errors.New("desktop stream is closed")

const (
	desktopBinaryEventType       = "nexterm.desktop-binary"
	desktopStreamClosedEventType = "nexterm.desktop-stream-closed"
)

type desktopBinaryEvent struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type desktopStreamClosedEvent struct {
	Type string `json:"type"`
}

type desktopStreamWindow interface {
	DispatchWailsEvent(*application.CustomEvent)
}

type desktopStreamFactory struct {
	mu       sync.Mutex
	window   desktopStreamWindow
	channels map[string]*desktopStream
	// closed is atomic so dispatch can fail fast without taking mu while a
	// shutdown is in progress.
	closed atomic.Bool
	// closing is closed once by Close so senders already blocked in a window
	// dispatch can return.
	closing chan struct{}
}

type desktopStream struct {
	factory *desktopStreamFactory
	topic   string

	// mu only serializes dispatches on this channel; Close never takes it.
	mu             sync.Mutex
	closed         atomic.Bool
	closeEventSent atomic.Bool
}

func newDesktopStreamFactory() *desktopStreamFactory {
	return &desktopStreamFactory{channels: make(map[string]*desktopStream), closing: make(chan struct{})}
}

func (f *desktopStreamFactory) SetWindow(window desktopStreamWindow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.window = window
}

func (f *desktopStreamFactory) OpenBinary(ctx context.Context, channel ipc.ChannelRef) (ipc.BinaryStream, error) {
	return f.open(ctx, channel)
}

func (f *desktopStreamFactory) OpenJSON(ctx context.Context, channel ipc.ChannelRef) (ipc.JSONStream, error) {
	return f.open(ctx, channel)
}

func (f *desktopStreamFactory) open(ctx context.Context, channel ipc.ChannelRef) (*desktopStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if channel.ID == "" {
		return nil, ipc.BadParam(errors.New("通道 ID 不能为空"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed.Load() {
		return nil, errDesktopStreamClosed
	}
	if f.window == nil {
		return nil, ipc.ErrStreamsUnavailable
	}
	if _, exists := f.channels[channel.ID]; exists {
		return nil, fmt.Errorf("desktop channel %s already has an active producer", channel.ID)
	}
	stream := &desktopStream{factory: f, topic: "channel://" + channel.ID}
	f.channels[channel.ID] = stream
	return stream, nil
}

func (f *desktopStreamFactory) Close() error {
	if !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(f.closing)
	f.mu.Lock()
	streams := make([]*desktopStream, 0, len(f.channels))
	for _, stream := range f.channels {
		streams = append(streams, stream)
	}
	f.mu.Unlock()
	for _, stream := range streams {
		_ = stream.Close()
	}
	return nil
}

func (f *desktopStreamFactory) remove(channelID string, stream *desktopStream) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channels[channelID] == stream {
		delete(f.channels, channelID)
	}
}

func (f *desktopStreamFactory) dispatch(event *application.CustomEvent) error {
	if f.closed.Load() {
		return errDesktopStreamClosed
	}
	f.mu.Lock()
	window := f.window
	f.mu.Unlock()
	if window == nil {
		return ipc.ErrStreamsUnavailable
	}
	// The window call runs in its own goroutine so that Close can release a sender
	// that was already blocked inside it; an abandoned call lives until the window closes.
	done := make(chan struct{})
	go func() {
		defer close(done)
		window.DispatchWailsEvent(event)
	}()
	select {
	case <-done:
		return nil
	case <-f.closing:
		return errDesktopStreamClosed
	}
}

func (s *desktopStream) SendBinary(ctx context.Context, data []byte) error {
	err := s.send(ctx, desktopBinaryEvent{
		Type: desktopBinaryEventType,
		Data: base64.StdEncoding.EncodeToString(data),
	})
	if err == nil {
		diagcapture.RecordEvent("stream:binary", len(data))
	}
	return err
}

func (s *desktopStream) SendJSON(ctx context.Context, data json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return ipc.BadParam(fmt.Errorf("invalid JSON stream event: %w", err))
	}
	if decoder.More() {
		return ipc.BadParam(errors.New("invalid JSON stream event: multiple values"))
	}
	err := s.send(ctx, payload)
	if err == nil {
		diagcapture.RecordEvent("stream:json", len(data))
	}
	return err
}

func (s *desktopStream) send(ctx context.Context, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() || s.factory.closed.Load() {
		return errDesktopStreamClosed
	}
	err := s.dispatchLocked(ctx, payload)
	// A Close that raced with this dispatch could not take mu; hand the closed
	// event over to whichever side releases the lock last.
	s.flushClosedEvent()
	return err
}

func (s *desktopStream) dispatchLocked(ctx context.Context, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return errDesktopStreamClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.factory.dispatch(&application.CustomEvent{Name: s.topic, Data: payload})
}

// flushClosedEvent emits the closed event once, after any in-flight data
// event. If another sender holds mu, that sender flushes after it unlocks.
func (s *desktopStream) flushClosedEvent() {
	if !s.closed.Load() || s.factory.closed.Load() {
		return
	}
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	if !s.closeEventSent.CompareAndSwap(false, true) {
		return
	}
	_ = s.factory.dispatch(&application.CustomEvent{
		Name: s.topic, Data: desktopStreamClosedEvent{Type: desktopStreamClosedEventType},
	})
}

func (s *desktopStream) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.factory.remove(s.topic[len("channel://"):], s)
	s.flushClosedEvent()
	return nil
}

var _ ipc.StreamFactory = (*desktopStreamFactory)(nil)
var _ ipc.BinaryStream = (*desktopStream)(nil)
var _ ipc.JSONStream = (*desktopStream)(nil)
