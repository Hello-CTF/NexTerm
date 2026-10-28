package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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

// legacyAttachProvider models a device whose bridge reaches an older helper:
// the provider resolves, and every checked attach meets the legacy helper.
type legacyAttachProvider struct {
	*listingDurableProvider
	legacy  atomic.Bool
	attachs atomic.Int32
}

func (p *legacyAttachProvider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, offset uint64) (base.DurableAttachment, error) {
	if p.legacy.Load() {
		p.attachs.Add(1)
		return nil, fmt.Errorf("attach: %w", legacyHelperErr())
	}
	return p.listingDurableProvider.AttachFromWithIdentity(ctx, id, identity, offset)
}

func TestDeviceReconnectBesideALegacyHelperEndsEveryDurableTabOnce(t *testing.T) {
	inner := &legacyAttachProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	provider := &faultCreateDurableProvider{DurableProvider: inner}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{provider: provider},
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "device/legacy-attach", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	var tabs []string
	for _, channel := range []string{"device-a", "device-b"} {
		info, err := manager.OpenTab(ctx, OpenTabOptions{
			SessionID: connected.ID, ClientID: "client-a", ChannelID: channel, Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{},
		})
		if err != nil {
			t.Fatal(err)
		}
		tabs = append(tabs, info.ID)
	}
	connector.connect = func(_ context.Context, asset Asset, generation uint64, _ int) (base.Transport, error) {
		return newFakeTransport(asset.Kind, generation+1000), nil
	}
	inner.legacy.Store(true)
	provider.attachment(0).fail(fmt.Errorf("active attach: %w", base.ErrDisconnected))
	waitFor(t, func() bool {
		status := connected.Info()
		return status.Status == StatusConnected && status.Generation == 2
	})
	for _, id := range tabs {
		waitFor(t, func() bool { return infoExited(manager, id) })
		waitForTabMarker(t, manager, id, "旧版组件")
		if got := tabRawCount(t, manager, id, "helper restarted"); got != 0 {
			t.Fatalf("tab %s shows the helper restarted banner", id)
		}
	}
	if got := inner.attachs.Load(); got != 1 {
		t.Fatalf("reconnect attached %d times beside the legacy helper, want 1: the first legacy answer is final", got)
	}
	if status := connected.Info(); status.Status != StatusConnected || status.Generation != 2 {
		t.Fatalf("session after the legacy answer = %+v, want it connected without further reconnects", status)
	}
	if told := manager.LegacyHelperReplaced(ctx, connected.ID); told != 2 {
		t.Fatalf("replacement told %d tabs, want 2", told)
	}
}

func TestHelperReplaceFailureReportsItsStage(t *testing.T) {
	for _, test := range []struct {
		err  *supervisor.HelperReplaceError
		want []string
	}{
		{&supervisor.HelperReplaceError{Stage: supervisor.ReplaceVerify, Cause: errors.New("x")}, []string{"无法核实", "未做任何更改"}},
		{&supervisor.HelperReplaceError{Stage: supervisor.ReplaceTerminate, Cause: errors.New("x")}, []string{"未能结束旧版组件", "仍在运行"}},
		{&supervisor.HelperReplaceError{Stage: supervisor.ReplaceStart, Stopped: true, Forced: true, RolledBack: true, Cause: errors.New("x")}, []string{"会话已终止", "已重新启动旧版组件", "强制结束"}},
		{&supervisor.HelperReplaceError{Stage: supervisor.ReplaceStart, Stopped: true, Cause: errors.New("x")}, []string{"会话已终止", "未能启动"}},
	} {
		got := IPCError(fmt.Errorf("ensure: %w", test.err))
		if got.Code != ipc.Code("helper_replace_failed") {
			t.Fatalf("%v: code %s, want helper_replace_failed", test.err, got.Code)
		}
		for _, want := range test.want {
			if !strings.Contains(got.Message, want) {
				t.Fatalf("%v: message %q lacks %q", test.err, got.Message, want)
			}
		}
		detail, ok := got.Detail.(*HelperReplaceFailure)
		if !ok || detail.Stage != string(test.err.Stage) || detail.Stopped != test.err.Stopped || detail.Forced != test.err.Forced || detail.RolledBack != test.err.RolledBack {
			t.Fatalf("%v: detail %#v", test.err, got.Detail)
		}
	}
}
