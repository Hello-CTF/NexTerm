package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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
	closed   bool
}

type desktopStream struct {
	factory *desktopStreamFactory
	topic   string

	mu     sync.Mutex
	closed bool
}

func newDesktopStreamFactory() *desktopStreamFactory {
	return &desktopStreamFactory{channels: make(map[string]*desktopStream)}
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
	if f.closed {
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
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
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
	f.mu.Lock()
	window := f.window
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return errDesktopStreamClosed
	}
	if window == nil {
		return ipc.ErrStreamsUnavailable
	}
	window.DispatchWailsEvent(event)
	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errDesktopStreamClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.factory.dispatch(&application.CustomEvent{Name: s.topic, Data: payload})
}

func (s *desktopStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.factory.remove(s.topic[len("channel://"):], s)
	_ = s.factory.dispatch(&application.CustomEvent{
		Name: s.topic, Data: desktopStreamClosedEvent{Type: desktopStreamClosedEventType},
	})
	return nil
}

var _ ipc.StreamFactory = (*desktopStreamFactory)(nil)
var _ ipc.BinaryStream = (*desktopStream)(nil)
var _ ipc.JSONStream = (*desktopStream)(nil)
