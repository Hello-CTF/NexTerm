package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const testDesktopBinaryType = "nexterm.desktop-binary"

type encodingStreamWindow struct {
	mu       sync.Mutex
	retain   bool
	events   [][]byte
	lastSize int
}

func (w *encodingStreamWindow) DispatchWailsEvent(event *application.CustomEvent) {
	encoded := []byte(event.ToJSON())
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastSize = len(encoded)
	if w.retain {
		w.events = append(w.events, encoded)
	}
}

func (w *encodingStreamWindow) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.events...)
}

func (w *encodingStreamWindow) encodedSize() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSize
}

func desktopBinaryBytes(data any) ([]byte, bool) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, false
	}
	var payload struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil || payload.Type != testDesktopBinaryType {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.Data)
	return decoded, err == nil
}

func decodeDesktopBinaryData(t *testing.T, data any) []byte {
	t.Helper()
	decoded, ok := desktopBinaryBytes(data)
	if !ok {
		t.Fatalf("binary payload is not typed base64: %#v", data)
	}
	return decoded
}

func decodeDesktopWireEvent(t *testing.T, encoded []byte) (string, []byte) {
	t.Helper()
	var event struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(encoded, &event); err != nil {
		t.Fatalf("decode Wails event: %v", err)
	}
	var payload struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("decode binary payload: %v", err)
	}
	if payload.Type != testDesktopBinaryType {
		t.Fatalf("binary payload type = %q", payload.Type)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		t.Fatalf("decode binary base64: %v", err)
	}
	return event.Name, decoded
}

func TestDesktopStreamBinary64KiBUsesTypedBase64(t *testing.T) {
	window := &encodingStreamWindow{retain: true}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "binary-64k"})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 64<<10)
	for index := range frame {
		frame[index] = byte(index*31 + index/251)
	}
	if err := stream.SendBinary(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	events := window.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	name, decoded := decodeDesktopWireEvent(t, events[0])
	if name != "channel://binary-64k" {
		t.Fatalf("event name = %q", name)
	}
	if !bytes.Equal(decoded, frame) {
		t.Fatal("64 KiB frame changed during Wails JSON round trip")
	}
	if maxSize := base64.StdEncoding.EncodedLen(len(frame)) + 256; len(events[0]) > maxSize {
		t.Fatalf("encoded event = %d bytes, want at most %d", len(events[0]), maxSize)
	}
	t.Logf("64 KiB frame encoded as %d byte Wails event", len(events[0]))
}

func TestDesktopStreamFourMiBReplayPreservesEveryFrame(t *testing.T) {
	const (
		frameSize  = 64 << 10
		frameCount = 64
	)
	window := &encodingStreamWindow{retain: true}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "replay-4m"})
	if err != nil {
		t.Fatal(err)
	}
	source := make([]byte, frameSize*frameCount)
	for index := range source {
		source[index] = byte(index*31 + index/frameSize)
	}
	for offset := 0; offset < len(source); offset += frameSize {
		if err := stream.SendBinary(context.Background(), source[offset:offset+frameSize]); err != nil {
			t.Fatalf("send frame %d: %v", offset/frameSize, err)
		}
	}
	events := window.snapshot()
	if len(events) != frameCount {
		t.Fatalf("events = %d, want %d", len(events), frameCount)
	}
	var replayed bytes.Buffer
	for index, encoded := range events {
		name, decoded := decodeDesktopWireEvent(t, encoded)
		if name != "channel://replay-4m" {
			t.Fatalf("frame %d event name = %q", index, name)
		}
		expected := source[index*frameSize : (index+1)*frameSize]
		if !bytes.Equal(decoded, expected) {
			t.Fatalf("frame %d differs, got %d bytes", index, len(decoded))
		}
		replayed.Write(decoded)
	}
	if replayed.Len() != len(source) || !bytes.Equal(replayed.Bytes(), source) {
		t.Fatalf("replay = %d bytes, want unchanged ordered %d bytes", replayed.Len(), len(source))
	}
}

type countingMarshalPayload struct {
	calls int
}

func (p *countingMarshalPayload) MarshalJSON() ([]byte, error) {
	p.calls++
	return []byte(`{"counted":true}`), nil
}

func TestDesktopStreamWailsEncodingHappensOnce(t *testing.T) {
	window := &encodingStreamWindow{retain: true}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "encoding-count"})
	if err != nil {
		t.Fatal(err)
	}
	payload := &countingMarshalPayload{}
	if err := stream.(*desktopStream).send(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if payload.calls != 1 {
		t.Fatalf("payload JSON encodings = %d, want 1", payload.calls)
	}
	if events := window.snapshot(); len(events) != 1 {
		t.Fatalf("Wails encoded events = %d, want 1", len(events))
	}
}

func BenchmarkDesktopStreamSendBinary64KiB(b *testing.B) {
	window := &encodingStreamWindow{}
	factory := newDesktopStreamFactory()
	factory.SetWindow(window)
	stream, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "benchmark-64k"})
	if err != nil {
		b.Fatal(err)
	}
	frame := make([]byte, 64<<10)
	for index := range frame {
		frame[index] = byte(index*31 + index/251)
	}
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := stream.SendBinary(context.Background(), frame); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(window.encodedSize()), "json_bytes")
}
