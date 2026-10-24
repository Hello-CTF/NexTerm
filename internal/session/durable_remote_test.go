package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type listingDurableProvider struct {
	*fakeDurableProvider
}

type descriptorListingDurableProvider struct {
	*listingDurableProvider
}

func (p *descriptorListingDurableProvider) ListDurableDescriptors(ctx context.Context) ([]base.DurableDescriptor, error) {
	ids, err := p.ListDurable(ctx)
	if err != nil {
		return nil, err
	}
	descriptors := make([]base.DurableDescriptor, len(ids))
	for index, id := range ids {
		descriptors[index].ID = id
	}
	return descriptors, nil
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
	t.Cleanup(func() { _ = manager.Close() })
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

func TestCreateTabSSHHelperFallbackPreservesNoticeAfterRename(t *testing.T) {
	resolver := &countingResolver{err: base.ErrUnsupported}
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-fallback", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.CreateTab(t.Context(), CreateTabOptions{SessionID: connected.ID, Title: "regular shell"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Durable || first.Title != "regular shell" || first.FallbackNotice == "" {
		t.Fatalf("first tab = %+v; want titled ordinary PTY and fallback notice", first)
	}
	second, err := manager.CreateTab(t.Context(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if second.Durable || second.FallbackNotice != "" || resolver.callCount() != 1 || connector.transport(0).channelCount() != 2 {
		t.Fatalf("second tab = %+v, resolver calls = %d, PTY channels = %d", second, resolver.callCount(), connector.transport(0).channelCount())
	}
}

func TestOpenTabSSHExplicitRetryAfterHelperBecomesAvailable(t *testing.T) {
	provider := newFakeDurableProvider()
	attempts := 0
	resolver := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		attempts++
		if attempts == 1 {
			return nil, durable.ErrUnavailable
		}
		return provider, nil
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-helper-retry", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	open := func(channel string, fallback bool) TabInfo {
		t.Helper()
		info, err := manager.OpenTab(t.Context(), OpenTabOptions{
			SessionID: connected.ID, ClientID: "client", ChannelID: channel, Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{}, DurableFallback: fallback,
		})
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if first := open("auto-first", true); first.Durable || first.FallbackNotice == "" {
		t.Fatalf("first tab = %+v; want ordinary PTY with fallback notice", first)
	}
	if second := open("auto-second", true); second.Durable || attempts != 1 {
		t.Fatalf("second tab = %+v, attempts = %d; automatic startup should use short failure cache", second, attempts)
	}
	if retried := open("explicit-retry", false); !retried.Durable || attempts != 2 {
		t.Fatalf("retried tab = %+v, attempts = %d; explicit request must probe helper again", retried, attempts)
	}
}

func TestOpenTabSSHDeploysHelperInBackgroundForLaterTabs(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	started := make(chan struct{})
	release := make(chan struct{})
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		close(started)
		select {
		case <-release:
			return provider, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	})
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = manager.Close()
	})
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-background-helper", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	type openResult struct {
		info TabInfo
		err  error
	}
	opened := make(chan openResult, 1)
	go func() {
		info, err := manager.OpenTab(context.Background(), OpenTabOptions{
			SessionID: connected.ID, ClientID: "client", ChannelID: "ordinary-first", Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{}, DurableFallback: true,
		})
		opened <- openResult{info: info, err: err}
	}()
	firstID := ""
	select {
	case result := <-opened:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.info.Durable || result.info.FallbackNotice == "" {
			t.Fatalf("first tab = %+v; want ordinary SSH while helper deploys", result.info)
		}
		firstID = result.info.ID
	case <-time.After(time.Second):
		close(release)
		t.Fatal("ordinary SSH open waited for helper deployment")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background helper deployment did not start")
	}
	close(release)
	waitFor(t, func() bool {
		connected.durableMu.Lock()
		defer connected.durableMu.Unlock()
		return connected.durableProvider == provider
	})
	second, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "durable-later", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Durable {
		t.Fatalf("later tab = %+v; want newly opened durable tab after background readiness", second)
	}
	firstTab, err := manager.Tab(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if firstTab.Info().Durable {
		t.Fatal("existing ordinary tab was mutated into a durable tab")
	}
}

func TestCreateTabSSHDeploysHelperInBackgroundForLaterTabs(t *testing.T) {
	provider := newFakeDurableProvider()
	started := make(chan struct{})
	release := make(chan struct{})
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		close(started)
		select {
		case <-release:
			return provider, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = manager.Close()
	})
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-create-background", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	type createResult struct {
		info TabInfo
		err  error
	}
	created := make(chan createResult, 1)
	go func() {
		info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
		created <- createResult{info: info, err: err}
	}()
	var first TabInfo
	select {
	case result := <-created:
		if result.err != nil {
			t.Fatal(result.err)
		}
		first = result.info
	case <-time.After(time.Second):
		close(release)
		t.Fatal("detached create waited for helper deployment")
	}
	if first.Durable || first.FallbackNotice == "" {
		t.Fatalf("first detached tab = %+v; want ordinary SSH", first)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background deployment did not start")
	}
	close(release)
	waitFor(t, func() bool {
		connected.durableMu.Lock()
		defer connected.durableMu.Unlock()
		return connected.durableProvider == provider
	})
	second, err := manager.CreateTab(t.Context(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Durable {
		t.Fatalf("later detached tab = %+v; want durable", second)
	}
	firstTab, err := manager.Tab(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstTab.Info().Durable {
		t.Fatal("existing ordinary detached tab was mutated")
	}
}

func TestAutomaticDurableProviderReconnectDoesNotRegisterOldGeneration(t *testing.T) {
	var resolverMu sync.Mutex
	resolverCalls := 0
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		resolverMu.Lock()
		resolverCalls++
		resolverMu.Unlock()
		<-ctx.Done()
		return nil, context.Cause(ctx)
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-register-reconnect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	transport := connected.transport
	generation := connected.generation
	connected.mu.Unlock()
	connected.durableMu.Lock()
	type resolveResult struct {
		provider base.DurableProvider
		err      error
	}
	result := make(chan resolveResult, 1)
	go func() {
		provider, err := manager.automaticDurableProvider(context.Background(), connected, transport, generation)
		result <- resolveResult{provider: provider, err: err}
	}()
	if err := manager.Reconnect(t.Context(), connected.ID); err != nil {
		t.Fatal(err)
	}
	connected.durableMu.Unlock()
	select {
	case resolved := <-result:
		if !errors.Is(resolved.err, ErrStaleGeneration) || resolved.provider != nil {
			t.Fatalf("stale automatic registration = %+v, %v", resolved.provider, resolved.err)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic registration did not finish")
	}
	resolverMu.Lock()
	calls := resolverCalls
	resolverMu.Unlock()
	if calls != 0 {
		t.Fatalf("old generation bound the reconnected context and started %d resolvers", calls)
	}
	backgroundHelperDeployments.Lock()
	entry := backgroundHelperDeployments.entries[connected]
	backgroundHelperDeployments.Unlock()
	if entry != nil {
		t.Fatalf("stale deployment remained registered: %+v", entry)
	}
}

func TestOpenTabSSHBackgroundDeploymentUsesSessionLifetime(t *testing.T) {
	started := make(chan struct{})
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		close(started)
		<-ctx.Done()
		return nil, context.Cause(ctx)
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-background-cancel", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	openCtx, cancelOpen := context.WithCancel(t.Context())
	info, err := manager.OpenTab(openCtx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "ordinary", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	cancelOpen()
	if err != nil || info.Durable {
		t.Fatalf("ordinary open = %+v, %v", info, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background deployment did not start")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	completedHelperDeployments.Lock()
	_, retained := completedHelperDeployments.entries[connected]
	completedHelperDeployments.Unlock()
	if retained {
		t.Fatal("closed session retained a completed helper operation")
	}
}

func TestCreateTabSSHFailedCommandDoesNotConsumeFallbackNotice(t *testing.T) {
	resolver := &countingResolver{err: base.ErrUnsupported}
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-fallback-command", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connector.transport(0).writeErr = errors.New("PTY write failed")
	if _, err := manager.CreateTab(t.Context(), CreateTabOptions{SessionID: connected.ID, Command: "echo ready"}); err == nil {
		t.Fatal("failed startup command unexpectedly created a tab")
	}
	connector.transport(0).writeErr = nil
	info, err := manager.CreateTab(t.Context(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if info.FallbackNotice == "" || info.Durable || resolver.callCount() != 1 {
		t.Fatalf("successful retry = %+v, helper attempts = %d", info, resolver.callCount())
	}
}

func TestOpenTabSSHDoesNotFallbackAfterUnclassifiedHelperError(t *testing.T) {
	resolverErr := errors.New("helper request rejected")
	resolver := &countingResolver{err: resolverErr}
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-helper-rejected", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if !errors.Is(err, resolverErr) || connector.transport(0).channelCount() != 0 {
		t.Fatalf("OpenTab = %v, PTY channels = %d; want original error and no fallback", err, connector.transport(0).channelCount())
	}
}

func TestOpenTabSSHCreateUnavailableFallsBackAndCachesFailure(t *testing.T) {
	provider := &fakeDurableProvider{records: make(map[string]*fakeDurableRecord), createErr: durable.ErrUnavailable}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-create-fallback", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		info, err := manager.OpenTab(t.Context(), OpenTabOptions{
			SessionID: connected.ID, ClientID: "client", ChannelID: []string{"first", "second"}[index], Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{}, DurableFallback: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if info.Durable || (info.FallbackNotice != "") != (index == 0) {
			t.Fatalf("attempt %d tab = %+v; want ordinary PTY and first notice only", index, info)
		}
	}
	if provider.createCalls != 1 || resolver.callCount() != 1 || connector.transport(0).channelCount() != 2 {
		t.Fatalf("create calls = %d, resolver calls = %d, PTY channels = %d", provider.createCalls, resolver.callCount(), connector.transport(0).channelCount())
	}
	_, err = manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "strict", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if !errors.Is(err, durable.ErrUnavailable) || connector.transport(0).channelCount() != 2 {
		t.Fatalf("strict durable error = %v, PTY channels = %d", err, connector.transport(0).channelCount())
	}
	if provider.createCalls != 2 {
		t.Fatalf("strict durable Create calls = %d; explicit request must retry after automatic fallback", provider.createCalls)
	}
}

func TestOpenTabSSHCreateFailureKeepsExistingDurableTabsUsable(t *testing.T) {
	provider := &descriptorListingDurableProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	resolver := &countingResolver{provider: provider}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-existing-durable", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	old, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "existing", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.createErr = durable.ErrUnavailable
	provider.mu.Unlock()
	fallback, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "fallback", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil || fallback.Durable || fallback.FallbackNotice == "" {
		t.Fatalf("fallback = %+v, %v; want ordinary PTY with notice", fallback, err)
	}
	renamed, err := manager.RenameTab(t.Context(), old.ID, "still available")
	if err != nil || renamed.Title != "still available" {
		t.Fatalf("rename existing durable = %+v, %v", renamed, err)
	}
	descriptors, err := manager.ListRemoteDurable(t.Context(), connected.ID)
	if err != nil || len(descriptors) != 1 || descriptors[0].ID != old.ID {
		t.Fatalf("listed existing durable tabs = %+v, %v", descriptors, err)
	}
	owners, err := manager.RemoteDurableOwners(t.Context(), old.ID, []string{connected.ID})
	if err != nil || len(owners) != 1 || owners[0] != connected.ID {
		t.Fatalf("existing durable owners = %v, %v", owners, err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d; existing provider should stay cached", resolver.callCount())
	}
	provider.mu.Lock()
	provider.createErr = nil
	provider.mu.Unlock()
	strict, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "strict-retry", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil || !strict.Durable {
		t.Fatalf("explicit durable retry = %+v, %v", strict, err)
	}
	resumed, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "auto-resumed", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil || !resumed.Durable {
		t.Fatalf("automatic durable after successful retry = %+v, %v", resumed, err)
	}
}

func TestOpenTabSSHFallbackNoticeRepeatsAfterReconnect(t *testing.T) {
	resolver := &countingResolver{err: durable.ErrUnavailable}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-notice-reconnect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	open := func(channel string) TabInfo {
		t.Helper()
		info, err := manager.OpenTab(t.Context(), OpenTabOptions{
			SessionID: connected.ID, ClientID: "client", ChannelID: channel, Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{}, DurableFallback: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if info := open("before"); info.FallbackNotice == "" {
		t.Fatal("first fallback omitted notice")
	}
	if err := manager.Reconnect(t.Context(), connected.ID); err != nil {
		t.Fatal(err)
	}
	if info := open("after"); info.FallbackNotice == "" {
		t.Fatal("fallback after reconnect omitted notice")
	}
}

func TestOpenTabSSHCreateUnclassifiedErrorDoesNotFallback(t *testing.T) {
	provider := &fakeDurableProvider{records: make(map[string]*fakeDurableRecord), createErr: errors.New("create rejected")}
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{provider: provider}})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-create-rejected", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	transport := connected.transport
	generation := connected.generation
	connected.mu.Unlock()
	if _, err := manager.durableProviderFor(t.Context(), connected, transport, KindSSH, generation, false); err != nil {
		t.Fatal(err)
	}
	_, err = manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err == nil || connector.transport(0).channelCount() != 0 {
		t.Fatalf("create error = %v, PTY channels = %d; unexpected fallback", err, connector.transport(0).channelCount())
	}
}

func TestOpenTabSSHDurableUnsupported(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
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

func TestOpenTabDeviceUsesRemoteDurableProvider(t *testing.T) {
	provider := &descriptorListingDurableProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	resolver := &countingResolver{provider: provider}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: resolver,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/dev-1", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Durable {
		t.Fatalf("tab = %+v; want durable", first)
	}
	descriptors, err := manager.ListRemoteDurable(ctx, connected.ID)
	if err != nil || len(descriptors) != 1 || descriptors[0].ID != first.ID {
		t.Fatalf("device durable discovery = %+v, %v; want the opened tab", descriptors, err)
	}
	if provider.createCalls != 1 {
		t.Fatalf("provider create calls = %d; want 1", provider.createCalls)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-2", Cols: 80, Rows: 24,
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
	reconnected, err := manager.Connect(ctx, Asset{ID: "device/dev-1", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: reconnected.ID, ClientID: "client-a", ChannelID: "device-3", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	}); err != nil {
		t.Fatal(err)
	}
	if resolver.callCount() != 2 {
		t.Fatalf("resolver calls = %d; want 2 after reconnect", resolver.callCount())
	}
	descriptors, err = manager.ListRemoteDurable(ctx, reconnected.ID)
	if err != nil || len(descriptors) != 3 {
		t.Fatalf("device durable discovery after reconnect = %+v, %v; want existing tabs", descriptors, err)
	}
}

func TestOpenTabSSHDurableResolverError(t *testing.T) {
	resolverErr := errors.New("daemon unavailable")
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{err: resolverErr},
	})
	t.Cleanup(func() { _ = manager.Close() })
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
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-a", Kind: KindSSH}); err != nil {
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

func TestRecoverRemoteDurableRequiresScopeWhenHostsShareID(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	tabID := "0123456789abcdef0123456789abcdef"
	if _, err := provider.Create(t.Context(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
	})
	t.Cleanup(func() { _ = manager.Close() })
	first, err := manager.Connect(t.Context(), Asset{ID: "ssh-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Connect(t.Context(), Asset{ID: "ssh-b", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	_, err = manager.RecoverRemoteDurable(t.Context(), tabID, OpenTabOptions{ClientID: "client-a", ChannelID: "ambiguous", Cols: 80, Rows: 24})
	if !errors.Is(err, ErrAmbiguousDurable) || provider.attachCalls != 0 {
		t.Fatalf("unscoped recovery = %v, attach calls = %d", err, provider.attachCalls)
	}
	info, err := manager.RecoverRemoteDurable(t.Context(), tabID, OpenTabOptions{
		SessionID: first.ID, ClientID: "client-a", ChannelID: "scoped", Cols: 80, Rows: 24,
	})
	if err != nil || info.SessionID != first.ID {
		t.Fatalf("scoped recovery = %+v, %v", info, err)
	}
}

func TestRecoverRemoteDurableUsesUniqueReachableOwner(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	tabID := "0123456789abcdef0123456789abcdef"
	if _, err := provider.Create(t.Context(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	connector := newFakeConnector()
	resolver := DurableResolverFunc(func(_ context.Context, transport base.Transport) (base.DurableProvider, error) {
		if transport == connector.transport(0) {
			return provider, nil
		}
		return nil, durable.ErrUnavailable
	})
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	owner, err := manager.Connect(t.Context(), Asset{ID: "ssh-owner", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	unavailable, err := manager.Connect(t.Context(), Asset{ID: "ssh-unavailable", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	owners, err := manager.RemoteDurableOwners(t.Context(), tabID, []string{owner.ID, unavailable.ID})
	if err != nil || len(owners) != 1 || owners[0] != owner.ID {
		t.Fatalf("owners = %v, %v; want reachable owner", owners, err)
	}
	info, err := manager.RecoverRemoteDurable(t.Context(), tabID, OpenTabOptions{ClientID: "client", ChannelID: "reachable-owner", Cols: 80, Rows: 24})
	if err != nil || info.ID != tabID || info.SessionID != owner.ID {
		t.Fatalf("recovered tab = %+v, %v; want reachable owner", info, err)
	}
}

func TestRecoverRemoteDurableDoesNotHideUnrelatedSearchError(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	tabID := "0123456789abcdef0123456789abcdef"
	if _, err := provider.Create(t.Context(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	connector := newFakeConnector()
	listErr := errors.New("daemon enumeration failed")
	resolver := DurableResolverFunc(func(_ context.Context, transport base.Transport) (base.DurableProvider, error) {
		if transport == connector.transport(0) {
			return provider, nil
		}
		return nil, listErr
	})
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	owner, err := manager.Connect(t.Context(), Asset{ID: "ssh-owner", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	unavailable, err := manager.Connect(t.Context(), Asset{ID: "ssh-search-error", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RemoteDurableOwners(t.Context(), tabID, []string{owner.ID, unavailable.ID}); !errors.Is(err, listErr) {
		t.Fatalf("owner search error = %v; want enumeration failure", err)
	}
	if _, err := manager.RecoverRemoteDurable(t.Context(), tabID, OpenTabOptions{ClientID: "client", ChannelID: "search-error", Cols: 80, Rows: 24}); !errors.Is(err, listErr) {
		t.Fatalf("recovery error = %v; want enumeration failure", err)
	}
}

func TestRecoverRemoteDurableIncludesDeviceSessions(t *testing.T) {
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
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	if _, err := manager.Connect(ctx, Asset{ID: "device/dev-1", Kind: KindDevice}); err != nil {
		t.Fatal(err)
	}
	info, err := manager.RecoverRemoteDurable(ctx, tabID, OpenTabOptions{ClientID: "client-a", ChannelID: "recover-device-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != tabID {
		t.Fatalf("recovered tab = %+v", info)
	}
	if provider.attachCalls != 1 {
		t.Fatalf("provider attach calls = %d; want 1", provider.attachCalls)
	}
}

type failingListDurableProvider struct {
	*fakeDurableProvider
	err error
}

func (p *failingListDurableProvider) ListDurable(context.Context) ([]string, error) {
	return nil, p.err
}

func TestRecoverRemoteDurableResolverErrorPropagates(t *testing.T) {
	resolverErr := errors.New("daemon unavailable")
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{err: resolverErr},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-resolver-error", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	_, err := manager.RecoverRemoteDurable(ctx, "0123456789abcdef0123456789abcdef", OpenTabOptions{ClientID: "client-a", ChannelID: "recover-resolver-error", Cols: 80, Rows: 24})
	if errors.Is(err, ErrTabNotFound) {
		t.Fatalf("RecoverRemoteDurable error = %v; resolver failures must not collapse into ErrTabNotFound", err)
	}
	if !errors.Is(err, resolverErr) {
		t.Fatalf("RecoverRemoteDurable error = %v; want the resolver error", err)
	}
}

func TestRecoverRemoteDurableListErrorPropagates(t *testing.T) {
	listErr := errors.New("daemon enumeration failed")
	provider := &failingListDurableProvider{fakeDurableProvider: newFakeDurableProvider(), err: listErr}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-list-error", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	_, err := manager.RecoverRemoteDurable(ctx, "0123456789abcdef0123456789abcdef", OpenTabOptions{ClientID: "client-a", ChannelID: "recover-list-error", Cols: 80, Rows: 24})
	if errors.Is(err, ErrTabNotFound) {
		t.Fatalf("RecoverRemoteDurable error = %v; enumeration failures must not collapse into ErrTabNotFound", err)
	}
	if !errors.Is(err, listErr) {
		t.Fatalf("RecoverRemoteDurable error = %v; want the enumeration error", err)
	}
}

func TestRecoverRemoteDurableJoinsSearchErrors(t *testing.T) {
	firstErr := errors.New("first daemon unreachable")
	secondErr := errors.New("second daemon unreachable")
	var calls int
	resolver := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		calls++
		if calls == 1 {
			return nil, firstErr
		}
		return nil, secondErr
	})
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: resolver,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-join-a", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Connect(ctx, Asset{ID: "ssh-join-b", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	_, err := manager.RecoverRemoteDurable(ctx, "0123456789abcdef0123456789abcdef", OpenTabOptions{ClientID: "client-a", ChannelID: "recover-join", Cols: 80, Rows: 24})
	if errors.Is(err, ErrTabNotFound) {
		t.Fatalf("RecoverRemoteDurable error = %v; search failures must not collapse into ErrTabNotFound", err)
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("RecoverRemoteDurable error = %v; want both daemon failures joined", err)
	}
}

func TestHelperOperationCancelAndRetryFenceGenerationAndOperation(t *testing.T) {
	type attempt struct {
		id      string
		started chan struct{}
		ctx     context.Context
	}
	var mu sync.Mutex
	attempts := make([]*attempt, 0, 2)
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		mu.Lock()
		index := len(attempts)
		current := &attempt{id: []string{"operation-one", "operation-two"}[index], started: make(chan struct{}), ctx: ctx}
		attempts = append(attempts, current)
		mu.Unlock()
		ReportHelperOperation(ctx, current.id)
		close(current.started)
		<-ctx.Done()
		return nil, context.Cause(ctx)
	})
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-helper-operation", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "ordinary-operation", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil || opened.Durable {
		t.Fatalf("ordinary tab = %+v, %v", opened, err)
	}
	mu.Lock()
	first := attempts[0]
	mu.Unlock()
	select {
	case <-first.started:
	case <-time.After(time.Second):
		t.Fatal("first helper operation did not start")
	}
	if err := manager.CancelHelperOperation(t.Context(), connected.ID, "stale-operation"); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale cancellation = %v, want ErrStaleGeneration", err)
	}
	select {
	case <-first.ctx.Done():
		t.Fatal("stale operation ID canceled the active deployment")
	default:
	}
	if err := manager.CancelHelperOperation(t.Context(), connected.ID, first.id); err != nil {
		t.Fatalf("cancel helper operation: %v", err)
	}
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("real deployment context was not canceled")
	}
	if !connector.transport(0).IsAlive() {
		t.Fatal("helper cancellation disconnected SSH")
	}
	tab, err := manager.Tab(opened.ID)
	if err != nil || tab.Info().Durable {
		t.Fatalf("ordinary tab after cancellation = %+v, %v", tab, err)
	}
	waitFor(t, func() bool {
		backgroundHelperDeployments.Lock()
		defer backgroundHelperDeployments.Unlock()
		return backgroundHelperDeployments.entries[connected] == nil
	})
	if err := manager.RetryHelperOperation(t.Context(), connected.ID, first.id); err != nil {
		t.Fatalf("retry helper operation: %v", err)
	}
	mu.Lock()
	if len(attempts) != 2 {
		mu.Unlock()
		t.Fatalf("resolver attempts = %d, want a fresh operation", len(attempts))
	}
	second := attempts[1]
	mu.Unlock()
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("retried helper operation did not start")
	}
	ReportHelperOperation(first.ctx, "late-old-operation")
	if err := manager.CancelHelperOperation(t.Context(), connected.ID, first.id); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old operation cancellation after retry = %v, want ErrStaleGeneration", err)
	}
	select {
	case <-second.ctx.Done():
		t.Fatal("old operation cancellation stopped the retry")
	default:
	}
	if err := manager.CancelHelperOperation(t.Context(), connected.ID, second.id); err != nil {
		t.Fatalf("cancel retried operation: %v", err)
	}
}

func TestHelperOperationCompletionDoesNotRegress(t *testing.T) {
	started := make(chan struct{})
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		ReportHelperOperation(ctx, "retry-current")
		close(started)
		<-ctx.Done()
		return nil, context.Cause(ctx)
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-completion-order", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	transport := connected.transport
	generation := connected.generation
	connected.mu.Unlock()
	completed := func(sequence uint64, operationID string) *backgroundHelperDeployment {
		entry := &backgroundHelperDeployment{
			session: connected, transport: transport, generation: generation, sequence: sequence,
			operationID: operationID, done: make(chan struct{}), err: durable.ErrUnavailable,
		}
		close(entry.done)
		return entry
	}
	recordCompletedHelperDeployment(completed(2, "newer-operation"))
	recordCompletedHelperDeployment(completed(1, "older-operation"))
	if err := manager.RetryHelperOperation(t.Context(), connected.ID, "older-operation"); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("older completed operation was revived: %v", err)
	}
	if err := manager.RetryHelperOperation(t.Context(), connected.ID, "newer-operation"); err != nil {
		t.Fatalf("newer completed operation could not retry: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("current operation did not start")
	}
	if err := manager.CancelHelperOperation(t.Context(), connected.ID, "retry-current"); err != nil {
		t.Fatalf("cancel current operation: %v", err)
	}
}

func TestCancelHelperOperationDoesNotBlockNextAutomaticTab(t *testing.T) {
	started := make(chan struct{})
	provider := newFakeDurableProvider()
	var calls atomic.Int32
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		if calls.Add(1) == 1 {
			ReportHelperOperation(ctx, "cancel-next-auto")
			close(started)
			<-ctx.Done()
			return nil, errors.Join(fmt.Errorf("wrapped helper upload cancellation: %w", context.Cause(ctx)), context.Canceled)
		}
		return provider, nil
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-cancel-next-auto", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "cancel-first", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil || first.Durable {
		t.Fatalf("first ordinary tab = %+v, %v", first, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("deployment did not start")
	}
	backgroundHelperDeployments.Lock()
	deployment := backgroundHelperDeployments.entries[connected]
	backgroundHelperDeployments.Unlock()
	if deployment == nil {
		t.Fatal("deployment disappeared before cancellation")
	}
	if err := manager.CancelHelperOperation(t.Context(), connected.ID, "cancel-next-auto"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deployment.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled deployment did not complete")
	}
	connected.durableMu.Lock()
	t.Logf("durable failure after explicit cancellation: %v", connected.durableFailure)
	connected.durableMu.Unlock()
	second, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "cancel-second", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil {
		t.Fatalf("auto tab immediately after cancellation failed: %v", err)
	}
	if !second.Durable {
		t.Fatalf("auto tab after cancellation = %+v; want usable durable provider", second)
	}

	unsafeManager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), OpenAccess: true,
		DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
			return nil, errors.New("unclassified helper safety failure")
		}),
	})
	t.Cleanup(func() { _ = unsafeManager.Close() })
	unsafeSession, err := unsafeManager.Connect(t.Context(), Asset{ID: "ssh-unsafe-error", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unsafeManager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: unsafeSession.ID, ClientID: "client", ChannelID: "unsafe-auto", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	}); err == nil {
		t.Fatal("unclassified helper error unexpectedly fell back to ordinary SSH")
	}
}

func TestCancelHelperOperationPreservesNonCancellationFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		result error
	}{
		{name: "direct", result: errors.New("unclassified helper safety failure after user cancellation")},
		{name: "joined_cleanup", result: errors.Join(context.Canceled, errors.New("helper cleanup failed"))},
		{name: "joined_deadline", result: errors.Join(context.Canceled, context.DeadlineExceeded)},
	} {
		t.Run(test.name, func(t *testing.T) {
			operationID := "cancel-preserves-" + test.name
			started := make(chan struct{})
			var calls atomic.Int32
			resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
				if calls.Add(1) == 1 {
					ReportHelperOperation(ctx, operationID)
					close(started)
					<-ctx.Done()
					return nil, test.result
				}
				<-ctx.Done()
				return nil, context.Cause(ctx)
			})
			manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
			t.Cleanup(func() { _ = manager.Close() })
			connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-cancel-preserves-" + test.name, Kind: KindSSH})
			if err != nil {
				t.Fatal(err)
			}
			first, err := manager.OpenTab(t.Context(), OpenTabOptions{
				SessionID: connected.ID, ClientID: "client", ChannelID: "cancel-safety-first", Cols: 80, Rows: 24,
				Durable: &DurableTabOptions{}, DurableFallback: true,
			})
			if err != nil || first.Durable {
				t.Fatalf("first ordinary tab = %+v, %v", first, err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("deployment did not start")
			}
			backgroundHelperDeployments.Lock()
			deployment := backgroundHelperDeployments.entries[connected]
			backgroundHelperDeployments.Unlock()
			if deployment == nil {
				t.Fatal("deployment disappeared before cancellation")
			}
			if err := manager.CancelHelperOperation(t.Context(), connected.ID, operationID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-deployment.done:
			case <-time.After(time.Second):
				t.Fatal("cancelled deployment did not complete")
			}
			connected.durableMu.Lock()
			failure := connected.durableFailure
			connected.durableMu.Unlock()
			if failure != test.result {
				t.Fatalf("durable failure = %v, want preserved result %v", failure, test.result)
			}
			second, err := manager.OpenTab(t.Context(), OpenTabOptions{
				SessionID: connected.ID, ClientID: "client", ChannelID: "cancel-safety-second", Cols: 80, Rows: 24,
				Durable: &DurableTabOptions{}, DurableFallback: true,
			})
			if err == nil {
				t.Fatalf("auto tab after non-cancellation failure = %+v; want fail closed", second)
			}
			if err != test.result {
				t.Fatalf("auto tab error = %v, want preserved result %v", err, test.result)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("resolver calls after cached safety failure = %d, want 1", got)
			}
		})
	}
}

// An explicit durable request (for example listing background tabs) arriving while the helper deploys
// must join the single background deployment instead of deploying under durableMu, otherwise the first
// tab of the session stays blocked until the whole deployment ends.
func TestOpenTabSSHIsNotBlockedByExplicitDurableRequestDuringDeployment(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return provider, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = manager.Close()
	})
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-explicit-during-deploy", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	transport, generation := connected.transport, connected.generation
	connected.mu.Unlock()

	type providerResult struct {
		provider base.DurableProvider
		err      error
	}
	explicit := make(chan providerResult, 1)
	go func() {
		got, err := manager.durableProviderFor(context.Background(), connected, transport, KindSSH, generation, false)
		explicit <- providerResult{provider: got, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("explicit request did not start a deployment")
	}

	opened := make(chan error, 1)
	var info TabInfo
	go func() {
		var openErr error
		info, openErr = manager.OpenTab(context.Background(), OpenTabOptions{
			SessionID: connected.ID, ClientID: "client", ChannelID: "first", Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{}, DurableFallback: true,
		})
		opened <- openErr
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
		if info.Durable || info.FallbackNotice == "" {
			t.Fatalf("first tab = %+v; want an ordinary PTY while the helper deploys", info)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the first tab was blocked behind the deployment started by an explicit request")
	}

	close(release)
	select {
	case result := <-explicit:
		if result.err != nil || result.provider != provider {
			t.Fatalf("explicit request = %v, %v; want the deployed provider", result.provider, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit request did not finish after the deployment completed")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resolver ran %d times; want a single shared deployment", got)
	}
}

func TestListRemoteDurableDoesNotRedeployAfterARecordedFailure(t *testing.T) {
	resolver := &countingResolver{err: fmt.Errorf("%w: helper cannot run here", durable.ErrUnavailable)}
	var offset atomic.Int64
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver,
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).Add(time.Duration(offset.Load())) },
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-list-failure", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ListRemoteDurable(t.Context(), connected.ID); err == nil {
		t.Fatal("first listing succeeded; want the deployment failure")
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d; want 1", resolver.callCount())
	}
	offset.Store(int64(time.Minute))
	if _, err := manager.ListRemoteDurable(t.Context(), connected.ID); err == nil {
		t.Fatal("second listing succeeded; want the recorded failure")
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d after the cache window; a poll must not redeploy", resolver.callCount())
	}
}

// A deployment started by the call itself is the fresh attempt: with retryFailure it must not run twice.
func TestExplicitDurableRequestDeploysOnceWhenItStartsTheDeployment(t *testing.T) {
	resolver := &countingResolver{err: fmt.Errorf("%w: helper cannot run here", durable.ErrUnavailable)}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-retry-once", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	transport, generation := connected.transport, connected.generation
	connected.mu.Unlock()
	if _, err := manager.durableProviderFor(t.Context(), connected, transport, KindSSH, generation, true); err == nil {
		t.Fatal("deployment succeeded; want the failure")
	}
	if got := resolver.callCount(); got != 1 {
		t.Fatalf("resolver ran %d times; want 1 for a deployment this call started", got)
	}
}

// Cancelling a caller that is only waiting must not cancel the shared deployment.
func TestCancellingADurableWaiterDoesNotCancelTheDeployment(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		started <- struct{}{}
		select {
		case <-release:
			return provider, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = manager.Close()
	})
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-waiter-cancel", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	transport, generation := connected.transport, connected.generation
	connected.mu.Unlock()

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := manager.durableProviderFor(waiterCtx, connected, transport, KindSSH, generation, false)
		waiterDone <- err
	}()
	<-started
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v; want context.Canceled", err)
	}

	close(release)
	waitFor(t, func() bool {
		connected.durableMu.Lock()
		defer connected.durableMu.Unlock()
		return connected.durableProvider == provider
	})
}
