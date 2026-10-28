package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type faultCreateDurableProvider struct {
	base.DurableProvider
	mu          sync.Mutex
	attachments []*faultDurableAttachment
}

func (p *faultCreateDurableProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.DurableProvider.Create(ctx, options)
	if err != nil {
		return nil, err
	}
	fault := &faultDurableAttachment{DurableAttachment: attachment, failed: make(chan struct{})}
	p.mu.Lock()
	p.attachments = append(p.attachments, fault)
	p.mu.Unlock()
	return fault, nil
}

func (p *faultCreateDurableProvider) attachment(index int) *faultDurableAttachment {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attachments[index]
}

type faultDurableAttachment struct {
	base.DurableAttachment
	failed   chan struct{}
	failOnce sync.Once
	err      error
}

func (a *faultDurableAttachment) Identity() supervisor.Identity {
	identity, _ := durableAttachmentIdentity(a.DurableAttachment)
	return supervisor.Identity{CreatedAt: identity.CreatedAt, Incarnation: identity.Incarnation, Generation: identity.Generation}
}

func (a *faultDurableAttachment) Read(data []byte) (int, error) {
	type result struct {
		count int
		err   error
	}
	read := make(chan result, 1)
	go func() {
		count, err := a.DurableAttachment.Read(data)
		read <- result{count: count, err: err}
	}()
	select {
	case <-a.failed:
		return 0, a.err
	case result := <-read:
		return result.count, result.err
	}
}

func (a *faultDurableAttachment) Wait(ctx context.Context) error {
	select {
	case <-a.failed:
		return a.err
	default:
		return a.DurableAttachment.Wait(ctx)
	}
}

func (a *faultDurableAttachment) fail(err error) {
	a.failOnce.Do(func() {
		a.err = err
		close(a.failed)
	})
}

func TestDeviceTransportFailureReconnectsWithoutExitAndReplaysOnce(t *testing.T) {
	underlying := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	provider := &faultCreateDurableProvider{DurableProvider: underlying}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	log := &versionedEventLog{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver,
		Emitter: log.emitter(), ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/fault", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-fault", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := underlying.attachment(info.ID, 0).emit([]byte("before-transport-fault")); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, info.ID, "before-transport-fault")
	connector.connect = func(_ context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		if call == 2 {
			underlying.mu.Lock()
			record := underlying.records[info.ID]
			record.output = append(record.output, []byte("during-transport-fault")...)
			underlying.mu.Unlock()
		}
		return newFakeTransport(asset.Kind, generation+1000), nil
	}

	provider.attachment(0).fail(fmt.Errorf("active attach: %w", base.ErrDisconnected))
	waitFor(t, func() bool {
		status := connected.Info()
		return status.Status == StatusConnected && status.Generation == 2
	})

	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); got.ID != info.ID || got.SessionID != connected.ID || !got.Durable || got.Exited {
		t.Fatalf("tab after transport recovery = %+v; want same live durable tab", got)
	}
	assertNoTransportExit(t, log, info.ID)
	if create, attach, killed := underlying.counts(); create != 1 || attach != 1 || killed != 0 {
		t.Fatalf("durable calls after recovery: create=%d attach=%d killed=%d; want 1, 1, 0", create, attach, killed)
	}
	waitForTabMarker(t, manager, info.ID, "during-transport-fault")
	raw, err := manager.RawDump(info.ID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"before-transport-fault", "during-transport-fault"} {
		if count := strings.Count(string(raw), marker); count != 1 {
			t.Fatalf("%q appears %d times after recovery; want exactly once: %q", marker, count, raw)
		}
	}
}

func TestTransportFailureReconnectExhaustionKeepsManualRecovery(t *testing.T) {
	underlying := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	provider := &faultCreateDurableProvider{DurableProvider: underlying}
	connector := newFakeConnector()
	connector.connect = func(_ context.Context, asset Asset, generation uint64, call int) (base.Transport, error) {
		if call == 2 {
			return nil, errors.New("device still offline")
		}
		return newFakeTransport(asset.Kind, generation+1000), nil
	}
	log := &versionedEventLog{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider}, Emitter: log.emitter(),
		ReconnectMax: 1, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/exhausted", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-exhausted", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}

	provider.attachment(0).fail(base.ErrDisconnected)
	waitFor(t, func() bool { return connected.Info().Status == StatusFailed })
	if got := connected.Info().Generation; got != 2 {
		t.Fatalf("failed automatic reconnect generation = %d; want 2", got)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); got.Exited || !got.Durable {
		t.Fatalf("tab after reconnect exhaustion = %+v; want live durable state retained for manual recovery", got)
	}
	assertNoTransportExit(t, log, info.ID)
	if _, _, killed := underlying.counts(); killed != 0 {
		t.Fatalf("reconnect exhaustion killed %d durable processes; want 0", killed)
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatalf("manual reconnect after exhaustion: %v", err)
	}
	if got := connected.Info(); got.Status != StatusConnected || got.Generation != 3 {
		t.Fatalf("manual recovery session = %+v; want connected generation 3", got)
	}
	if got := tab.Info(); got.ID != info.ID || got.SessionID != connected.ID || got.Exited || !got.Durable {
		t.Fatalf("manually recovered tab = %+v; want same live durable tab", got)
	}
	assertNoTransportExit(t, log, info.ID)
	if create, attach, killed := underlying.counts(); create != 1 || attach != 1 || killed != 0 {
		t.Fatalf("durable calls after manual recovery: create=%d attach=%d killed=%d; want 1, 1, 0", create, attach, killed)
	}
}

func TestConcurrentTransportFailuresAdvanceOneGenerationWithoutExits(t *testing.T) {
	connector := newFakeConnector()
	log := &versionedEventLog{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: log.emitter(),
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "ssh-concurrent-fault", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabs := []*Tab{
		openTestTab(t, manager, connected, "client-a", "fault-a"),
		openTestTab(t, manager, connected, "client-a", "fault-b"),
		openTestTab(t, manager, connected, "client-a", "fault-c"),
	}
	transport := connector.transport(0)
	transport.alive.Store(false)
	var wg sync.WaitGroup
	for index := range tabs {
		wg.Add(1)
		go func(channel *fakeChannel) {
			defer wg.Done()
			_ = channel.Close()
		}(transport.channel(index))
	}
	wg.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		status := connected.Info()
		if status.Status == StatusConnected && status.Generation == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("concurrent recovery did not settle at generation 2: session=%+v connects=%d", status, connector.callCount())
		}
		time.Sleep(time.Millisecond)
	}
	waitFor(t, func() bool {
		for _, tab := range tabs {
			tab.mu.Lock()
			current := tab.generation == 2 && tab.channel != nil
			tab.mu.Unlock()
			if !current {
				return false
			}
		}
		return true
	})

	if got := connector.callCount(); got != 2 {
		t.Fatalf("concurrent failures caused %d connects; want initial plus one reconnect", got)
	}
	if got := connector.transport(1).channelCount(); got != len(tabs) {
		t.Fatalf("replacement PTY count = %d; want %d", got, len(tabs))
	}
	for _, tab := range tabs {
		if got := tab.Info(); got.Exited || got.Durable {
			t.Fatalf("ordinary SSH tab after recovery = %+v; want live non-durable replacement", got)
		}
		assertNoTransportExit(t, log, tab.ID)
	}
}

func TestAuthoritativeDurableExitEmitsOnceWithoutReconnect(t *testing.T) {
	underlying := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	provider := &faultCreateDurableProvider{DurableProvider: underlying}
	connector := newFakeConnector()
	log := &versionedEventLog{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider}, Emitter: log.emitter(),
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "device/real-exit", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-real-exit", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}

	underlying.finish(info.ID, 23)
	waitFor(t, func() bool {
		exits := log.exitsFor(info.ID)
		return len(exits) == 1 && infoExited(manager, info.ID)
	})
	exits := log.exitsFor(info.ID)
	if len(exits) != 1 || exits[0].ExitCode == nil || *exits[0].ExitCode != 23 {
		t.Fatalf("authoritative exit events = %+v; want one exit code 23", exits)
	}
	controls := log.controlsFor(info.ID)
	if len(controls) == 0 || !controls[len(controls)-1].Exited {
		t.Fatalf("authoritative exit controls = %+v; want final exited control", controls)
	}
	exitedControls := 0
	for _, control := range controls {
		if control.Exited {
			exitedControls++
		}
	}
	if exitedControls != 1 {
		t.Fatalf("exited control count = %d; want 1: %+v", exitedControls, controls)
	}
	if got := connector.callCount(); got != 1 {
		t.Fatalf("authoritative exit triggered reconnect: %d connects; want 1", got)
	}
	if got := connected.Info().Status; got != StatusConnected {
		t.Fatalf("healthy session after one durable exit = %s; want connected", got)
	}
}

func assertNoTransportExit(t *testing.T, log *versionedEventLog, tabID string) {
	t.Helper()
	if exits := log.exitsFor(tabID); len(exits) != 0 {
		t.Fatalf("transport failure emitted terminal exits for %s: %+v", tabID, exits)
	}
	for _, control := range log.controlsFor(tabID) {
		if control.Exited {
			t.Fatalf("transport failure emitted exited control for %s: %+v", tabID, control)
		}
	}
}
