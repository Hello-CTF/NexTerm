package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestAttachDurableReplacementKnownIdentityResumesFromOffset(t *testing.T) {
	provider := newFakeDurableProvider()
	created, err := provider.Create(context.Background(), base.DurableCreateOptions{ID: "known-identity", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := created.(*fakeDurableAttachment).emit([]byte("consumed-tail")); err != nil {
		t.Fatal(err)
	}
	identity, ok := durableAttachmentIdentity(created)
	if !ok {
		t.Fatal("created attachment has no identity")
	}
	attachment, err := attachDurableReplacement(context.Background(), provider, "known-identity", identity, uint64(len("consumed-")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	buffer := make([]byte, 64)
	count, err := attachment.Read(buffer)
	if err != nil || string(buffer[:count]) != "tail" {
		t.Fatalf("checked resume read = %q, %v; want the unconsumed suffix", buffer[:count], err)
	}
}

func TestAttachDurableReplacementIdentityMismatchDoesNotFallBack(t *testing.T) {
	provider := &identityMismatchProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	attachment, err := attachDurableReplacement(context.Background(), provider, "known-identity", base.DurableIdentity{Incarnation: "original"}, 12)
	if attachment != nil {
		_ = attachment.Close()
		t.Fatal("identity mismatch returned an attachment")
	}
	if !errors.Is(err, durable.ErrIdentity) {
		t.Fatalf("identity mismatch error = %v; want durable.ErrIdentity", err)
	}
	if provider.checkedCalls != 1 || provider.attachCalls != 0 {
		t.Fatalf("identity mismatch calls: checked=%d unchecked=%d; want 1, 0", provider.checkedCalls, provider.attachCalls)
	}
}

func TestAttachDurableReplacementWithoutIncarnationIsUnrecoverable(t *testing.T) {
	provider := newFakeDurableProvider()
	if _, err := provider.Create(context.Background(), base.DurableCreateOptions{ID: "legacy-tab", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	attachment, err := attachDurableReplacement(context.Background(), provider, "legacy-tab", base.DurableIdentity{}, 0)
	if attachment != nil {
		_ = attachment.Close()
		t.Fatal("a tab without an incarnation returned an attachment")
	}
	if !errors.Is(err, ErrDurableUnrecoverable) {
		t.Fatalf("missing incarnation error = %v; want ErrDurableUnrecoverable", err)
	}
	if _, attach, _ := provider.counts(); attach != 0 {
		t.Fatalf("missing incarnation reached the provider %d times; want 0", attach)
	}
}

func TestRecoverDurableWithoutIncarnationIsUnrecoverable(t *testing.T) {
	provider := newFakeDurableProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, first, firstSession, "legacy-recover", "a-1", false)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, _, _, secondSession := newDurableTestManager(t, provider)
	_, err := second.OpenTab(context.Background(), OpenTabOptions{
		TabID: info.ID, SessionID: secondSession.ID, ChannelID: "b-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true},
	})
	if !errors.Is(err, ErrDurableUnrecoverable) {
		t.Fatalf("recover without incarnation = %v; want ErrDurableUnrecoverable", err)
	}
	if _, attach, killed := provider.counts(); attach != 0 || killed != 0 {
		t.Fatalf("recover without incarnation: attach=%d killed=%d; want 0, 0", attach, killed)
	}
}

func TestReconnectDurableWithoutIncarnationExitsWithVisibleReason(t *testing.T) {
	underlying := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	provider := &faultCreateDurableProvider{DurableProvider: opaqueDurableProvider{underlying}}
	connector := newFakeConnector()
	log := &versionedEventLog{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{provider: provider},
		Emitter: log.emitter(), ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/legacy", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-legacy", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.connect = func(_ context.Context, asset Asset, generation uint64, _ int) (base.Transport, error) {
		return newFakeTransport(asset.Kind, generation+1000), nil
	}
	provider.attachment(0).fail(fmt.Errorf("active attach: %w", base.ErrDisconnected))
	waitFor(t, func() bool {
		status := connected.Info()
		return status.Status == StatusConnected && status.Generation == 2
	})
	waitFor(t, func() bool { return infoExited(manager, info.ID) })
	waitForTabMarker(t, manager, info.ID, "cannot be recovered")
	if _, attach, killed := underlying.counts(); attach != 0 || killed != 0 {
		t.Fatalf("unrecoverable reconnect: attach=%d killed=%d; want 0, 0", attach, killed)
	}
}

type identityMismatchProvider struct {
	*listingDurableProvider
	checkedCalls int
}

func (p *identityMismatchProvider) AttachFromWithIdentity(context.Context, string, base.DurableIdentity, uint64) (base.DurableAttachment, error) {
	p.checkedCalls++
	return nil, durable.ErrIdentity
}

func (p *identityMismatchProvider) AttachWithIdentity(context.Context, string, base.DurableIdentity) (base.DurableAttachment, error) {
	return nil, durable.ErrIdentity
}

// selectiveOpaqueProvider hides the supervisor identity of one tab's
// attachments, modelling a tab that never recorded an incarnation next to
// tabs that did.
type selectiveOpaqueProvider struct {
	*listingDurableProvider
	hidden string
}

func (p *selectiveOpaqueProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.listingDurableProvider.Create(ctx, options)
	if attachment == nil || options.ID != p.hidden {
		return attachment, err
	}
	return opaqueDurableAttachment{attachment}, err
}

func tabRawCount(t *testing.T, manager *Manager, tabID, marker string) int {
	t.Helper()
	raw, err := manager.RawDump(tabID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), marker)
}

// TestReconnectMixedTabsIsolatesUnrecoverableDurableTab proves one durable
// tab without an incarnation does not block its siblings: it exits with the
// visible reason while a checked durable tab resumes at its consumed offset
// and a plain tab gets a fresh PTY. A later reconnect never revives the lost
// durable tab as a plain PTY.
func TestReconnectMixedTabsIsolatesUnrecoverableDurableTab(t *testing.T) {
	underlying := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	provider := &selectiveOpaqueProvider{listingDurableProvider: underlying, hidden: "legacy-tab"}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{provider: provider},
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-mixed", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	open := func(tabID, channel string, durableTab bool) TabInfo {
		t.Helper()
		options := OpenTabOptions{TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: channel, Cols: 80, Rows: 24}
		if durableTab {
			options.Durable = &DurableTabOptions{}
		}
		info, err := manager.OpenTab(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	legacy := open("legacy-tab", "legacy", true)
	checked := open("checked-tab", "checked", true)
	plain := open("plain-tab", "plain", false)

	if err := underlying.attachment(checked.ID, 0).emit([]byte("checked-before")); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, checked.ID, "checked-before")
	underlying.mu.Lock()
	record := underlying.records[checked.ID]
	record.output = append(record.output, []byte("checked-during")...)
	underlying.mu.Unlock()

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return infoExited(manager, legacy.ID) })
	waitForTabMarker(t, manager, legacy.ID, "cannot be recovered")
	waitForTabMarker(t, manager, checked.ID, "checked-during")
	for _, marker := range []string{"checked-before", "checked-during"} {
		if got := tabRawCount(t, manager, checked.ID, marker); got != 1 {
			t.Fatalf("checked tab shows %q %d times; want exactly once after offset resume", marker, got)
		}
	}
	for _, id := range []string{checked.ID, plain.ID} {
		if infoExited(manager, id) {
			t.Fatalf("tab %s exited after a sibling was unrecoverable", id)
		}
	}
	if got := connector.transport(1).channelCount(); got != 1 {
		t.Fatalf("first reconnect opened %d plain PTYs; want 1 for the plain tab", got)
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	if got := connector.transport(2).channelCount(); got != 1 {
		t.Fatalf("second reconnect opened %d plain PTYs; want 1, the lost durable tab must not become a plain PTY", got)
	}
	tab, err := manager.Tab(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); !got.Exited || got.Durable {
		t.Fatalf("lost durable tab after second reconnect = %+v; want exited, not durable", got)
	}
	if got := tabRawCount(t, manager, legacy.ID, string(reconnectBanner)); got != 0 {
		t.Fatalf("lost durable tab received %d reconnect banners; want none", got)
	}
	if infoExited(manager, checked.ID) || infoExited(manager, plain.ID) {
		t.Fatal("second reconnect left a recoverable tab exited")
	}
}

// generationMismatchProvider models a helper that restarted after the tab
// attached: every checked attach is rejected for its helper generation.
type generationMismatchProvider struct {
	*listingDurableProvider
}

func (p *generationMismatchProvider) AttachFromWithIdentity(context.Context, string, base.DurableIdentity, uint64) (base.DurableAttachment, error) {
	return nil, fmt.Errorf("attach: %w", durable.ErrGenerationMismatch)
}

func TestReconnectDurableAfterHelperRestartExitsWithVisibleReason(t *testing.T) {
	provider := &faultCreateDurableProvider{DurableProvider: &generationMismatchProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}}
	connector := newFakeConnector()
	log := &versionedEventLog{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{provider: provider},
		Emitter: log.emitter(), ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/restarted", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "device-restarted", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.connect = func(_ context.Context, asset Asset, generation uint64, _ int) (base.Transport, error) {
		return newFakeTransport(asset.Kind, generation+1000), nil
	}
	provider.attachment(0).fail(fmt.Errorf("active attach: %w", base.ErrDisconnected))
	waitFor(t, func() bool {
		status := connected.Info()
		return status.Status == StatusConnected && status.Generation == 2
	})
	waitFor(t, func() bool { return infoExited(manager, info.ID) })
	waitForTabMarker(t, manager, info.ID, "helper restarted")
}
