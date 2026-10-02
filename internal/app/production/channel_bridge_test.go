//go:build (darwin || linux) && !race

package production

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/local"
)

type bridgeTestStream struct {
	mu     sync.Mutex
	id     string
	frames [][]byte
	closed bool
}

func (s *bridgeTestStream) SendBinary(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	s.frames = append(s.frames, append([]byte(nil), data...))
	return nil
}

func (s *bridgeTestStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *bridgeTestStream) output() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Join(s.frames, nil)
}

func (s *bridgeTestStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type bridgeTestFactory struct {
	mu      sync.Mutex
	streams map[string][]*bridgeTestStream
}

func (f *bridgeTestFactory) open(_ context.Context, channel ipc.ChannelRef) (ipc.BinaryStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stream := &bridgeTestStream{id: channel.ID}
	if f.streams == nil {
		f.streams = make(map[string][]*bridgeTestStream)
	}
	f.streams[channel.ID] = append(f.streams[channel.ID], stream)
	return stream, nil
}

func (f *bridgeTestFactory) at(channelID string, index int) *bridgeTestStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[channelID][index]
}

func TestProductionTerminalBridgeStreamsDetachAndReplayOnReopen(t *testing.T) {
	factory := &bridgeTestFactory{}
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	manager := session.NewManager(session.Config{Connector: connector})
	production, err := NewProductionWithServices(Config{
		Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
	}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "bridge-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	channel := ipc.ChannelRef{ID: "bridge-channel"}
	attachCtx, cancelAttach := context.WithCancel(t.Context())
	response := production.Dispatcher.Dispatch(attachCtx, ipc.Request{
		Command: "terminal_attach", Args: json.RawMessage(`{"sessionId":"` + connected.ID + `","cols":80,"rows":24}`), Channel: channel, ClientID: "client-a",
	}, production.Environment("client-a"))
	cancelAttach()
	if !response.OK {
		t.Fatalf("terminal_attach = %+v", response)
	}
	var tabID string
	if err := json.Unmarshal(response.Data, &tabID); err != nil {
		t.Fatal(err)
	}
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "terminal_write", Args: json.RawMessage(`{"args":{"tabId":"` + tabID + `","data":[112,114,105,110,116,102,32,39,110,101,120,116,101,114,109,45,98,114,105,100,103,101,45,111,107,92,110,39,13],"clientId":"client-a"}}`),
	}, production.Environment("client-a"))
	if !response.OK {
		t.Fatalf("terminal_write = %+v", response.Error)
	}
	first := factory.at(channel.ID, 0)
	waitForProductionOutput(t, first, "nexterm-bridge-ok")
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "terminal_detach", Args: json.RawMessage(`{"tabId":"` + tabID + `","channelId":"` + channel.ID + `"}`),
	}, production.Environment("client-a"))
	if !response.OK {
		t.Fatalf("terminal_detach = %+v", response.Error)
	}
	waitRetention(t, first.isClosed)
	response = production.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "terminal_attach_tab", Args: json.RawMessage(`{"tabId":"` + tabID + `","replayBytes":65536}`), Channel: channel, ClientID: "client-a",
	}, production.Environment("client-a"))
	if !response.OK {
		t.Fatalf("terminal_attach_tab = %+v", response.Error)
	}
	second := factory.at(channel.ID, 1)
	waitForProductionOutput(t, second, "nexterm-bridge-ok")
}

func waitForProductionOutput(t *testing.T, stream *bridgeTestStream, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !bytes.Contains(stream.output(), []byte(text)) {
		if time.Now().After(deadline) {
			t.Fatalf("stream %s output = %q, want %q", stream.id, stream.output(), text)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitRetention(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
