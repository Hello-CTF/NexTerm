//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type bridgeTestStream struct {
	mu      sync.Mutex
	id      string
	frames  [][]byte
	closed  bool
	changed chan struct{}
}

func newBridgeTestStream(id string) *bridgeTestStream {
	return &bridgeTestStream{id: id, changed: make(chan struct{})}
}

func (s *bridgeTestStream) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
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
	s.signalLocked()
	return nil
}

func (s *bridgeTestStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.signalLocked()
	s.mu.Unlock()
	return nil
}

func (s *bridgeTestStream) output() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Join(s.frames, nil)
}

type bridgeTestFactory struct {
	mu      sync.Mutex
	streams map[string][]*bridgeTestStream
}

func (f *bridgeTestFactory) open(_ context.Context, channel ipc.ChannelRef) (ipc.BinaryStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stream := newBridgeTestStream(channel.ID)
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
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, factory := newRemoteRecoveryHarness(t, resolver)
	connectRemoteSSHAsset(t, production)

	tabID := ids.New()
	bootstrap, err := fixture.provider.Create(t.Context(), base.DurableCreateOptions{
		ID: tabID, Command: []string{"sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}

	channelID := "bridge-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, channelID, "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachResponse, &attached)
	if attached.TabID != tabID || attached.Exited {
		t.Fatalf("attached tab = %+v, want live daemon tab %s", attached, tabID)
	}
	first := factory.at(channelID, 0)

	writeDurableTestInput(t, production, tabID, "printf 'nexterm-bridge-ok\\n'", "client-a")
	waitForProductionOutput(t, first, "nexterm-bridge-ok")

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_detach", `{"tabId":"`+tabID+`","channelId":"`+channelID+`"}`, "", "client-a"))
	waitForStreamClose(t, first)

	reopenResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":65536}`, channelID, "client-a")
	requireStoreTestResponse(t, reopenResponse, &attached)
	if attached.TabID != tabID || attached.Exited {
		t.Fatalf("reopened tab = %+v, want live daemon tab %s", attached, tabID)
	}
	second := factory.at(channelID, 1)
	waitForProductionOutput(t, second, "nexterm-bridge-ok")

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
}

func waitForProductionOutput(t *testing.T, stream *bridgeTestStream, text string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		stream.mu.Lock()
		if bytes.Contains(bytes.Join(stream.frames, nil), []byte(text)) {
			stream.mu.Unlock()
			return
		}
		changed := stream.changed
		stream.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("stream %s output = %q, want %q", stream.id, stream.output(), text)
		}
	}
}

func waitForStreamClose(t *testing.T, stream *bridgeTestStream) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		stream.mu.Lock()
		closed := stream.closed
		changed := stream.changed
		stream.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Fatal("stream was not closed after detach")
		}
	}
}
