//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type desktopRecoveryAnchor struct {
	Epoch        string
	StreamSeq    uint64
	ControlBytes int
	Expired      bool
}

type desktopRecoveryStream struct {
	mu      sync.Mutex
	id      string
	factory *desktopRecoveryFactory
	frames  [][]byte
	closed  bool
	changed chan struct{}

	block     chan struct{}
	entered   chan struct{}
	blockOnce *sync.Once

	anchor      desktopRecoveryAnchor
	streamBytes int
}

func newDesktopRecoveryStream(id string, factory *desktopRecoveryFactory) *desktopRecoveryStream {
	return &desktopRecoveryStream{id: id, factory: factory, changed: make(chan struct{})}
}

func (s *desktopRecoveryStream) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *desktopRecoveryStream) SendBinary(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	block, entered, once := s.block, s.entered, s.blockOnce
	s.mu.Unlock()
	if block != nil {
		once.Do(func() { close(entered) })
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	s.frames = append(s.frames, bytes.Clone(data))
	s.streamBytes += len(data)
	s.signalLocked()
	return nil
}

func (s *desktopRecoveryStream) SendJSON(_ context.Context, data json.RawMessage) error {
	var control struct {
		Type         string `json:"type"`
		Epoch        string `json:"epoch"`
		StreamSeq    uint64 `json:"streamSeq"`
		ControlBytes int    `json:"controlBytes"`
		Expired      bool   `json:"expired"`
	}
	if err := json.Unmarshal(data, &control); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	if control.Type == "nexterm.stream-anchor" {
		s.anchor = desktopRecoveryAnchor{
			Epoch: control.Epoch, StreamSeq: control.StreamSeq,
			ControlBytes: control.ControlBytes, Expired: control.Expired,
		}
		s.streamBytes = 0
	}
	s.signalLocked()
	return nil
}

func (s *desktopRecoveryStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.signalLocked()
	s.mu.Unlock()
	s.factory.closed <- s
	return nil
}

func (s *desktopRecoveryStream) arm() {
	s.mu.Lock()
	s.block = make(chan struct{})
	s.entered = make(chan struct{})
	s.blockOnce = new(sync.Once)
	s.mu.Unlock()
}

func (s *desktopRecoveryStream) release() {
	s.mu.Lock()
	if s.block != nil {
		close(s.block)
		s.block = nil
	}
	s.mu.Unlock()
}

func (s *desktopRecoveryStream) output() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Join(s.frames, nil)
}

func (s *desktopRecoveryStream) anchorSnapshot() desktopRecoveryAnchor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.anchor
}

func (s *desktopRecoveryStream) resume() (string, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	consumed := s.streamBytes - s.anchor.ControlBytes
	if consumed < 0 {
		consumed = 0
	}
	return s.anchor.Epoch, s.anchor.StreamSeq + uint64(consumed)
}

type desktopRecoveryFactory struct {
	mu      sync.Mutex
	streams map[string][]*desktopRecoveryStream
	closed  chan *desktopRecoveryStream
}

func newDesktopRecoveryFactory() *desktopRecoveryFactory {
	return &desktopRecoveryFactory{
		streams: make(map[string][]*desktopRecoveryStream),
		closed:  make(chan *desktopRecoveryStream, 4),
	}
}

func (f *desktopRecoveryFactory) open(_ context.Context, channel ipc.ChannelRef) (ipc.BinaryStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stream := newDesktopRecoveryStream(channel.ID, f)
	f.streams[channel.ID] = append(f.streams[channel.ID], stream)
	return stream, nil
}

func (f *desktopRecoveryFactory) at(channelID string, index int) *desktopRecoveryStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[channelID][index]
}

func waitDesktopRecovery(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newDesktopRecoveryProduction(t *testing.T, factory *desktopRecoveryFactory) (*Production, *session.Tab, *transcriptFeedTransport) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := hub.New(hub.Options{QueueFrames: 2, QueueBytes: 4 << 20})
	var transport *transcriptFeedTransport
	manager := session.NewManager(session.Config{
		Hub: bus, Logger: logger,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			transport = newTranscriptFeedTransport(asset.Kind, generation)
			return transport, nil
		}),
	})
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	production, err := NewProductionWithServices(Config{
		Logger: logger, Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
	}, ProductionServices{Store: database, Sessions: manager})
	if err != nil {
		_ = database.Close()
		_ = bus.Close()
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		_ = database.Close()
		_ = bus.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = production.Shutdown(context.Background())
		_ = bus.Close()
	})
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "desktop-recovery", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(t.Context(), session.OpenTabOptions{
		SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return production, tab, transport
}

func TestDesktopBridgeAutomaticallyReplaysAfterOverflowClose(t *testing.T) {
	factory := newDesktopRecoveryFactory()
	production, tab, transport := newDesktopRecoveryProduction(t, factory)
	channelID := "desktop-overflow-recovery"
	response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tab.ID+`"}`, channelID, "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, response, &attached)
	first := factory.at(channelID, 0)
	waitDesktopRecovery(t, "initial anchor and clear", func() bool {
		anchor := first.anchorSnapshot()
		return anchor.Epoch != "" && anchor.ControlBytes > 0 && len(first.output()) >= anchor.ControlBytes
	})
	initialAnchor := first.anchorSnapshot()
	source := first.output()

	prefix := bytes.Repeat([]byte("p"), 64<<10)
	source = append(source, prefix...)
	if err := transport.channel.send(prefix); err != nil {
		t.Fatal(err)
	}
	waitDesktopRecovery(t, "prefix on first desktop stream", func() bool { return len(first.output()) == len(source) })

	reopenDone := make(chan ipc.Response, 1)
	go func() {
		closed := <-factory.closed
		epoch, seq := closed.resume()
		args := fmt.Sprintf(`{"tabId":%q,"replayBytes":8388608,"fromSeq":%d,"fromEpoch":%q}`, tab.ID, seq, epoch)
		reopenDone <- production.Dispatcher.Dispatch(context.Background(), ipc.Request{
			Command: "terminal_attach_tab", Args: json.RawMessage(args),
			Channel: ipc.ChannelRef{ID: channelID}, ClientID: "client-a",
		}, production.Environment("client-a"))
	}()

	first.arm()
	for index := range 80 {
		chunk := bytes.Repeat([]byte{byte('a' + index)}, 64<<10)
		source = append(source, chunk...)
		if err := transport.channel.send(chunk); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			select {
			case <-first.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("desktop stream did not enter its send barrier")
			}
		}
	}
	waitDesktopRecovery(t, "overflow detach before old stream close", func() bool { return tab.Info().Subscribers == 0 })

	residual := []byte("desktop-residual-after-abort")
	source = append(source, residual...)
	if err := transport.channel.send(residual); err != nil {
		t.Fatal(err)
	}
	waitDesktopRecovery(t, "post-abort residual in the terminal ring", func() bool {
		return tab.ScreenSeq() == uint64(len(source)-initialAnchor.ControlBytes)
	})
	first.release()

	var reopened ipc.Response
	select {
	case reopened = <-reopenDone:
	case <-time.After(30 * time.Second):
		t.Fatal("desktop close did not trigger automatic reattach")
	}
	requireStoreTestResponse(t, reopened, &attached)
	second := factory.at(channelID, 1)
	oldStream := first.output()
	if bytes.Contains(oldStream, residual) {
		t.Fatal("post-abort residual advanced the old desktop stream across the gap")
	}
	if len(oldStream) > len(source) {
		t.Fatalf("old desktop stream = %d bytes, source = %d", len(oldStream), len(source))
	}
	wantReplay := source[len(oldStream):]
	waitDesktopRecovery(t, "replayed bytes on the replacement desktop stream", func() bool {
		return len(second.output()) >= len(wantReplay)
	})
	if got := second.output(); !bytes.Equal(got, wantReplay) {
		t.Fatalf("desktop replay = %d bytes, want exact continuous gap of %d bytes", len(got), len(wantReplay))
	}
	_, resumedSeq := first.resume()
	resumedAnchor := second.anchorSnapshot()
	if resumedAnchor.Expired || resumedAnchor.ControlBytes != 0 || resumedAnchor.StreamSeq != resumedSeq {
		t.Fatalf("replacement anchor = %+v, want continuous sequence %d", resumedAnchor, resumedSeq)
	}

	tail := bytes.Repeat([]byte("z"), 64<<10)
	source = append(source, tail...)
	if err := transport.channel.send(tail); err != nil {
		t.Fatal(err)
	}
	waitDesktopRecovery(t, "new live output after replay", func() bool {
		return len(second.output()) >= len(wantReplay)+len(tail)
	})
	recovered := append(bytes.Clone(oldStream), second.output()...)
	if !bytes.Equal(recovered, source) {
		t.Fatalf("old desktop stream + replay + live = %d bytes, want complete source of %d bytes", len(recovered), len(source))
	}
}
