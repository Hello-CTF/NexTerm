package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// hookedOpenTransport is a fake transport whose OpenPTY is test-controlled:
// the hook decides whether a replacement open blocks, succeeds, or fails.
type hookedOpenTransport struct {
	kind       string
	generation uint64
	alive      atomic.Bool
	closeCount atomic.Int32

	mu       sync.Mutex
	channels []*fakeChannel

	openHook func(ctx context.Context, transport *hookedOpenTransport) (base.Channel, error)
}

func newHookedOpenTransport(kind string, generation uint64, hook func(context.Context, *hookedOpenTransport) (base.Channel, error)) *hookedOpenTransport {
	transport := &hookedOpenTransport{kind: kind, generation: generation, openHook: hook}
	transport.alive.Store(true)
	return transport
}

func (t *hookedOpenTransport) Kind() string       { return t.kind }
func (t *hookedOpenTransport) Generation() uint64 { return t.generation }
func (t *hookedOpenTransport) IsAlive() bool      { return t.alive.Load() }
func (t *hookedOpenTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}

func (t *hookedOpenTransport) Close() error {
	if !t.alive.Swap(false) {
		return nil
	}
	t.closeCount.Add(1)
	t.mu.Lock()
	channels := append([]*fakeChannel(nil), t.channels...)
	t.mu.Unlock()
	for _, channel := range channels {
		_ = channel.Close()
	}
	return nil
}

func (t *hookedOpenTransport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	if t.openHook != nil {
		return t.openHook(ctx, t)
	}
	return t.openChannel(), nil
}

func (t *hookedOpenTransport) openChannel() base.Channel {
	channel := newFakeChannel(t.generation)
	t.mu.Lock()
	t.channels = append(t.channels, channel)
	t.mu.Unlock()
	return channel
}

func (t *hookedOpenTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{Stdout: "ran", ExitCode: new(int)}, nil
}

func (t *hookedOpenTransport) channel(index int) *fakeChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.channels[index]
}

// blockingAttachProvider is a fake durable provider whose Attach is
// test-controlled; Create and every other operation delegate to the embedded
// fake so the initial durable tab opens normally.
type blockingAttachProvider struct {
	*fakeDurableProvider
	attachHook func(ctx context.Context, id string) (base.DurableAttachment, error)
}

func (p *blockingAttachProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	if p.attachHook != nil {
		return p.attachHook(ctx, id)
	}
	return p.fakeDurableProvider.Attach(ctx, id)
}

func reconnectCancelTestManager(t *testing.T, connector Connector, resolver *countingResolver) *Manager {
	t.Helper()
	manager := NewManager(Config{
		Connector:        connector,
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  resolver,
		ReconnectMax:     10,
		ReconnectBackoff: []time.Duration{0},
		OpenAccess:       true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// A caller cancellation while a replacement PTY open is blocked must abort
// the open, never commit, and close the transport — the scoped open context
// carries the attempt's cancellation into the in-flight open without binding
// the committed channel's lifetime to the attempt.
func TestReconnectCancelDuringBlockedReplacementOpen(t *testing.T) {
	openEntered := make(chan struct{})
	var openOnce sync.Once
	var openAborted atomic.Bool
	transport := newHookedOpenTransport(KindSSH, 1001, func(ctx context.Context, _ *hookedOpenTransport) (base.Channel, error) {
		openOnce.Do(func() { close(openEntered) })
		select {
		case <-ctx.Done():
			openAborted.Store(true)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("replacement open was not cancelled by the attempt")
		}
	})
	var calls atomic.Int32
	connector := ConnectorFunc(func(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
		if calls.Add(1) == 1 {
			return newFakeTransport(asset.Kind, generation+1000), nil
		}
		return transport, nil
	})
	manager := reconnectCancelTestManager(t, connector, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	reconnectCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- manager.Reconnect(reconnectCtx, connected.ID) }()
	<-openEntered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("reconnect error = %v, want context.Canceled", err)
	}
	if !openAborted.Load() {
		t.Fatal("blocked replacement open was not aborted by caller cancellation")
	}
	if transport.IsAlive() {
		t.Fatal("replacement transport still alive after cancelled reconnect")
	}
	if status := connected.Info().Status; status == StatusConnected {
		t.Fatal("cancelled reconnect committed the session")
	}
}

// A caller cancellation that lands after the replacement open succeeded but
// before the commit must skip the commit and explicitly close every opened
// replacement plus the transport, returning the cancellation instead of nil.
func TestReconnectCancelAfterOpenBeforeCommit(t *testing.T) {
	openEntered := make(chan struct{})
	var openOnce sync.Once
	var cancelAttempt context.CancelFunc
	transport := newHookedOpenTransport(KindSSH, 1001, func(ctx context.Context, t *hookedOpenTransport) (base.Channel, error) {
		openOnce.Do(func() { close(openEntered) })
		cancelAttempt()
		// The open succeeds only once the attempt's cancellation has
		// propagated into the open context, so the cancellation deterministically
		// lands between the successful open and the commit.
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return t.openChannel(), nil
	})
	var calls atomic.Int32
	connector := ConnectorFunc(func(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
		if calls.Add(1) == 1 {
			return newFakeTransport(asset.Kind, generation+1000), nil
		}
		return transport, nil
	})
	manager := reconnectCancelTestManager(t, connector, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	reconnectCtx, cancel := context.WithCancel(ctx)
	cancelAttempt = cancel
	done := make(chan error, 1)
	go func() { done <- manager.Reconnect(reconnectCtx, connected.ID) }()
	<-openEntered
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("reconnect error = %v, want context.Canceled", err)
	}
	if got := transport.channel(0).closeCount.Load(); got == 0 {
		t.Fatal("opened replacement was not closed after cancelled reconnect")
	}
	if transport.IsAlive() {
		t.Fatal("replacement transport still alive after cancelled reconnect")
	}
	if status := connected.Info().Status; status == StatusConnected {
		t.Fatal("cancelled reconnect committed the session")
	}
}

// A caller cancellation while a durable reattach is blocked must abort the
// attach through the scoped open context, never commit, and close the
// transport.
func TestReconnectCancelDuringBlockedDurableAttach(t *testing.T) {
	attachEntered := make(chan struct{})
	var attachOnce sync.Once
	var attachAborted atomic.Bool
	provider := &blockingAttachProvider{fakeDurableProvider: newFakeDurableProvider()}
	provider.attachHook = func(ctx context.Context, _ string) (base.DurableAttachment, error) {
		attachOnce.Do(func() { close(attachEntered) })
		select {
		case <-ctx.Done():
			attachAborted.Store(true)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("durable attach was not cancelled by the attempt")
		}
	}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	manager := reconnectCancelTestManager(t, connector, resolver)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-durable", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	}); err != nil {
		t.Fatal(err)
	}

	reconnectCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- manager.Reconnect(reconnectCtx, connected.ID) }()
	<-attachEntered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("reconnect error = %v, want context.Canceled", err)
	}
	if !attachAborted.Load() {
		t.Fatal("blocked durable attach was not aborted by caller cancellation")
	}
	if connector.transport(1).IsAlive() {
		t.Fatal("replacement transport still alive after cancelled reconnect")
	}
	if status := connected.Info().Status; status == StatusConnected {
		t.Fatal("cancelled reconnect committed the session")
	}
}
