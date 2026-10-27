package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func legacyHelperErr() error {
	helper := supervisor.RunningHelper{Protocol: 2, PID: 77, Activity: supervisor.ActivitySnapshot{PTY: 2, Exec: 1}, ActivityKnown: true}
	return fmt.Errorf("resolve: %w", &supervisor.LegacyHelperError{Helper: helper, ConsentToken: supervisor.HelperConsentToken(helper)})
}

func TestReconnectBesideALegacyHelperEndsDurableTabsWithTheLegacyBanner(t *testing.T) {
	provider := &faultCreateDurableProvider{DurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver,
		ReconnectBackoff: []time.Duration{0},
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
	resolver.mu.Lock()
	resolver.provider, resolver.err = nil, legacyHelperErr()
	resolver.mu.Unlock()
	provider.attachment(0).fail(fmt.Errorf("active attach: %w", base.ErrDisconnected))
	waitFor(t, func() bool {
		status := connected.Info()
		return status.Status == StatusConnected && status.Generation == 2
	})
	waitFor(t, func() bool { return infoExited(manager, info.ID) })
	waitForTabMarker(t, manager, info.ID, "旧版组件")
	if got := tabRawCount(t, manager, info.ID, "helper restarted"); got != 0 {
		t.Fatalf("legacy tab shows the helper restarted banner %d times", got)
	}
	if got := tabRawCount(t, manager, info.ID, "已终止"); got != 0 {
		t.Fatal("the terminated banner appeared before the user agreed to replace the helper")
	}

	if told := manager.LegacyHelperReplaced(ctx, connected.ID); told != 1 {
		t.Fatalf("replacement told %d tabs, want 1", told)
	}
	waitForTabMarker(t, manager, info.ID, "已终止")
	if told := manager.LegacyHelperReplaced(ctx, connected.ID); told != 0 {
		t.Fatalf("a second replacement told %d tabs again", told)
	}
}

func TestLegacyHelperErrorCarriesTheConsentDetail(t *testing.T) {
	err := IPCError(legacyHelperErr())
	if err.Code != ipc.Code("helper_upgrade_required") {
		t.Fatalf("code = %s", err.Code)
	}
	detail, ok := err.Detail.(*LegacyHelperDetail)
	if !ok || detail.PTYSessions != 2 || detail.ExecSessions != 1 || !detail.ActivityKnown || detail.ConsentToken == "" || detail.PID != 77 {
		t.Fatalf("detail = %#v, want the legacy helper's sessions and consent token", err.Detail)
	}
	if plain := IPCError(fmt.Errorf("x: %w", supervisor.ErrHelperUpgradeRequired)); plain.Detail != nil {
		t.Fatalf("an old peer without legacy detail got detail %#v", plain.Detail)
	}
}
