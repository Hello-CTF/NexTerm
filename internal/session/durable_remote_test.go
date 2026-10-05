package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type listingDurableProvider struct {
	*fakeDurableProvider
}

func (p *listingDurableProvider) ListDurable(context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.records))
	for id, record := range p.records {
		if record.killed == 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type countingResolver struct {
	mu       sync.Mutex
	calls    int
	provider base.DurableProvider
	err      error
}

func (r *countingResolver) ResolveDurable(context.Context, base.Transport) (base.DurableProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.provider, r.err
}

func (r *countingResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestOpenTabSSHUsesRemoteDurableProvider(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector:       connector,
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: resolver,
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-remote", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "remote-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Durable {
		t.Fatalf("tab = %+v; want durable", first)
	}
	if provider.createCalls != 1 {
		t.Fatalf("provider create calls = %d; want 1", provider.createCalls)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "remote-2", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	}); err != nil {
		t.Fatal(err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d; want 1 (cached per session)", resolver.callCount())
	}
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	reconnected, err := manager.Connect(ctx, Asset{ID: "ssh-remote", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: reconnected.ID, ClientID: "client-a", ChannelID: "remote-3", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	}); err != nil {
		t.Fatal(err)
	}
	if resolver.callCount() != 2 {
		t.Fatalf("resolver calls = %d; want 2 after reconnect", resolver.callCount())
	}
}

func TestOpenTabSSHDurableUnsupported(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory()})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-plain", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "plain-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("OpenTab error = %v; want ErrUnsupported", err)
	}
}

func TestOpenTabSSHDurableResolverError(t *testing.T) {
	resolverErr := errors.New("daemon unavailable")
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{err: resolverErr},
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-error", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "error-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if !errors.Is(err, resolverErr) {
		t.Fatalf("OpenTab error = %v; want the resolver error", err)
	}
}

func TestRecoverRemoteDurable(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	tabID := "0123456789abcdef0123456789abcdef"
	if _, err := provider.Create(context.Background(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	resolver := &countingResolver{provider: provider}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: resolver,
	})
	ctx := context.Background()
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-a", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-b", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	info, err := manager.RecoverRemoteDurable(ctx, tabID, OpenTabOptions{ClientID: "client-a", ChannelID: "recover-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != tabID {
		t.Fatalf("recovered tab = %+v", info)
	}
	if provider.attachCalls != 1 {
		t.Fatalf("provider attach calls = %d; want 1", provider.attachCalls)
	}
	if _, err := manager.RecoverRemoteDurable(ctx, "ffffffffffffffffffffffffffffffff", OpenTabOptions{ClientID: "client-a", ChannelID: "recover-2", Cols: 80, Rows: 24}); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("RecoverRemoteDurable error = %v; want ErrTabNotFound", err)
	}
}
