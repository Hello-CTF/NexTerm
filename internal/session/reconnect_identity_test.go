package session

import (
	"context"
	"errors"
	"fmt"
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
