package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func controlEventRecorder(events chan<- ControlEvent) Emitter {
	return EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic == TopicTerminalControl {
			events <- event.Payload.(ControlEvent)
		}
		return nil
	})
}

func newGuestControlManager(t *testing.T, events chan ControlEvent) (*Manager, *fakeConnector) {
	t.Helper()
	connector := newFakeConnector()
	config := Config{Connector: connector, Terminals: newFakeTerminalFactory()}
	if events != nil {
		config.Emitter = controlEventRecorder(events)
	}
	manager := NewManager(config)
	t.Cleanup(func() { _ = manager.Close() })
	return manager, connector
}

func openGuestArbitrationTab(t *testing.T, manager *Manager, session *Session) *Tab {
	t.Helper()
	tab := openTestTab(t, manager, session, "owner-client", "owner-1")
	if err := manager.DetachChannel("owner-1"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("precondition: controller = %q, want empty", info.Controller)
	}
	return tab
}

func guestLeaseState(tab *Tab) (string, time.Time) {
	tab.mu.Lock()
	defer tab.mu.Unlock()
	return tab.guestLease, tab.guestLeaseUntil
}

func forceExpireGuestLease(t *testing.T, tab *Tab) {
	t.Helper()
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.guestLease == "" {
		t.Fatal("precondition: no guest lease to expire")
	}
	tab.guestLeaseUntil = time.Now().Add(-time.Second)
}

func drainControlEvents(events chan ControlEvent) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

// TestWriteGuestTakesControlAndWrites 钉住基本路径: 无控制者时访客写入即取得
// 控制权 (controller=guest, grid 与 Claim 同语义, 发控制事件), 字节落到 PTY
// 通道, 同一访客再次写入是续租 (租约到期时刻前进, 不重复发事件); 租约过期后
// 同一访客仍可续租恢复。
func TestWriteGuestTakesControlAndWrites(t *testing.T) {
	events := make(chan ControlEvent, 64)
	manager, connector := newGuestControlManager(t, events)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-basic", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	channel := connector.transport(0).channel(0)

	drainControlEvents(events)
	before := time.Now()
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := string(channel.written()); got != "ls\n" {
		t.Fatalf("pty input = %q, want %q", got, "ls\n")
	}
	if info := tab.Info(); info.Controller != "preview-share1" {
		t.Fatalf("controller = %q, want preview-share1", info.Controller)
	}
	lease, until := guestLeaseState(tab)
	if lease != "preview-share1" || !until.After(before) {
		t.Fatalf("lease = %q until %v, want preview-share1 after %v", lease, until, before)
	}
	event := <-events
	if event.Controller != "preview-share1" {
		t.Fatalf("control event controller = %q, want preview-share1", event.Controller)
	}

	// 同一访客续租: 到期时刻前进, 不再发控制事件。
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("pwd\n"), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := string(channel.written()); got != "ls\npwd\n" {
		t.Fatalf("pty input = %q, want %q", got, "ls\npwd\n")
	}
	_, until2 := guestLeaseState(tab)
	if !until2.After(until) {
		t.Fatalf("renewal did not advance lease: %v -> %v", until, until2)
	}
	select {
	case unexpected := <-events:
		t.Fatalf("renewal emitted unexpected control event: %+v", unexpected)
	default:
	}

	// 租约过期后同一访客仍可写入恢复。
	forceExpireGuestLease(t, tab)
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("again\n"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "preview-share1" {
		t.Fatalf("controller after expired renew = %q", info.Controller)
	}
}

// TestWriteGuestOwnerActive 钉住所有者活跃仲裁: controller 被他人占有时访客
// 写入返回 ErrGuestControlOwner, 字节不落通道, 不记录租约。
func TestWriteGuestOwnerActive(t *testing.T) {
	manager, connector := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-owner", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	channel := connector.transport(0).channel(0)

	err = manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Minute)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("WriteGuest = %v, want ErrGuestControlOwner", err)
	}
	if got := channel.written(); len(got) != 0 {
		t.Fatalf("rejected write reached pty: %q", got)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("rejected write recorded lease %q", lease)
	}
	if info := tab.Info(); info.Controller != "owner-client" {
		t.Fatalf("controller = %q, want owner-client", info.Controller)
	}
}

// TestWriteGuestGuestBusyUntilLeaseExpiry 钉住访客间仲裁: 控制中的访客挡回
// 其他访客 (ErrGuestControlOwner); 其断线后 controller 被 orphan 清理但租约
// 仍在, 其他访客得 ErrGuestControlBusy; 租约过期被惰性清理后可立即接管。
func TestWriteGuestGuestBusyUntilLeaseExpiry(t *testing.T) {
	manager, connector := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-busy", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	channel := connector.transport(0).channel(0)

	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "preview-a", ChannelID: "spec-a"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("a\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("write while guest controls = %v, want ErrGuestControlOwner", err)
	}

	if err := manager.DetachChannel("spec-a"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("controller after guest disconnect = %q, want empty", info.Controller)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlBusy) {
		t.Fatalf("write during another guest lease = %v, want ErrGuestControlBusy", err)
	}
	if got := string(channel.written()); got != "a\n" {
		t.Fatalf("rejected write reached pty: %q", got)
	}

	forceExpireGuestLease(t, tab)
	if err := manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "preview-b" {
		t.Fatalf("controller = %q, want preview-b", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "preview-b" {
		t.Fatalf("lease = %q, want preview-b", lease)
	}
	if got := string(channel.written()); got != "a\nb\n" {
		t.Fatalf("pty input = %q, want %q", got, "a\nb\n")
	}
}

// TestWriteGuestOwnerClaimPreempts 钉住所有者夺回: Claim 立即清访客租约并接管,
// 访客再写入即得 ErrGuestControlOwner。
func TestWriteGuestOwnerClaimPreempts(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-preempt", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	previous, err := manager.Claim(tab.ID, "owner-client")
	if err != nil {
		t.Fatal(err)
	}
	if previous != "preview-share1" {
		t.Fatalf("Claim previous = %q, want preview-share1", previous)
	}
	if info := tab.Info(); info.Controller != "owner-client" {
		t.Fatalf("controller = %q, want owner-client", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("Claim left guest lease %q", lease)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("ls\n"), time.Hour)
	if !errors.Is(err, ErrGuestControlOwner) {
		t.Fatalf("WriteGuest after owner claim = %v, want ErrGuestControlOwner", err)
	}
}

// TestReleaseGuest 钉住释放语义: 交还控制权 (grid 观察者语义 + 控制事件) 并
// 清租约; 重复调用与不相关访客调用都是幂等 no-op; 释放后其他访客立即接管。
func TestReleaseGuest(t *testing.T) {
	events := make(chan ControlEvent, 64)
	manager, _ := newGuestControlManager(t, events)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-release", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("a\n"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReleaseGuest(tab.ID, "preview-b"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "preview-a" {
		t.Fatalf("unrelated ReleaseGuest changed controller to %q", info.Controller)
	}

	drainControlEvents(events)
	if err := manager.ReleaseGuest(tab.ID, "preview-a"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("controller after release = %q, want empty", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("release left lease %q", lease)
	}
	if event := <-events; event.Controller != "" {
		t.Fatalf("release event controller = %q, want empty", event.Controller)
	}

	if err := manager.ReleaseGuest(tab.ID, "preview-a"); err != nil {
		t.Fatalf("idempotent ReleaseGuest = %v", err)
	}
	select {
	case unexpected := <-events:
		t.Fatalf("idempotent ReleaseGuest emitted unexpected event: %+v", unexpected)
	default:
	}

	if err := manager.WriteGuest(ctx, tab.ID, "preview-b", []byte("b\n"), time.Hour); err != nil {
		t.Fatalf("guest take-over after release: %v", err)
	}
	if info := tab.Info(); info.Controller != "preview-b" {
		t.Fatalf("controller = %q, want preview-b", info.Controller)
	}
}

// TestWriteGuestDisconnectCleanup 钉住断线交互: 访客订阅通道断开时 orphan 清理
// 摘掉 controller 但保留租约 (重连宽限), 同一访客可立即恢复, 其他访客被 busy
// 挡住, ReleaseGuest 显式清租约后其他访客立即可写。
func TestWriteGuestDisconnectCleanup(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-disconnect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "preview-share1", ChannelID: "spec-1"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("x"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("spec-1"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("controller after disconnect = %q, want empty", info.Controller)
	}
	if lease, _ := guestLeaseState(tab); lease != "preview-share1" {
		t.Fatalf("lease after disconnect = %q, want preview-share1 (grace window)", lease)
	}

	// 同一访客在宽限窗内恢复控制。
	if err := manager.WriteGuest(ctx, tab.ID, "preview-share1", []byte("x"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "preview-share1" {
		t.Fatalf("controller after resume = %q", info.Controller)
	}

	// 再次断线后显式释放租约, 其他访客立即接管。
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "preview-share1", ChannelID: "spec-2"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("spec-2"); err != nil {
		t.Fatal(err)
	}
	err = manager.WriteGuest(ctx, tab.ID, "preview-other", []byte("y"), time.Hour)
	if !errors.Is(err, ErrGuestControlBusy) {
		t.Fatalf("other guest during grace window = %v, want ErrGuestControlBusy", err)
	}
	if err := manager.ReleaseGuest(tab.ID, "preview-share1"); err != nil {
		t.Fatal(err)
	}
	if lease, _ := guestLeaseState(tab); lease != "" {
		t.Fatalf("ReleaseGuest left lease %q", lease)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-other", []byte("y"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "preview-other" {
		t.Fatalf("controller = %q, want preview-other", info.Controller)
	}
}

// TestWriteGuestUnsupportedKinds 钉住能力边界: KindRemote 壳与非 ptyBacked
// (WinRM) 标签页一律 ErrUnsupported, 即使 controller 空闲或被占有。
func TestWriteGuestUnsupportedKinds(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()

	remote, err := manager.RegisterRemoteSession(ctx, RemoteSessionOptions{OwnerID: "user-1", Title: "relay", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, remote.ID, "preview-a", []byte("x"), time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("remote WriteGuest = %v, want ErrUnsupported", err)
	}
	if err := manager.ReleaseGuest(remote.ID, "preview-a"); err != nil {
		t.Fatalf("remote ReleaseGuest = %v, want nil no-op", err)
	}

	connected, err := manager.Connect(ctx, Asset{ID: "guest-winrm", Kind: KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("x"), time.Minute); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("winrm WriteGuest = %v, want ErrUnsupported", err)
	}
}

// TestWriteGuestClosedTab 钉住关闭边界: 标签页关闭后写与释放都拒绝。
func TestWriteGuestClosedTab(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-closed", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteGuest(ctx, tab.ID, "preview-a", []byte("x"), time.Minute); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("WriteGuest on closed tab = %v, want ErrTabNotFound", err)
	}
	if err := manager.ReleaseGuest(tab.ID, "preview-a"); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("ReleaseGuest on closed tab = %v, want ErrTabNotFound", err)
	}
}

// TestWriteGuestConcurrentArbitration 并发摇动访客写/释放与所有者
// Claim/Release, 配合 -race 暴露租约字段的并发访问问题。
func TestWriteGuestConcurrentArbitration(t *testing.T) {
	manager, _ := newGuestControlManager(t, nil)
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "guest-race", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openGuestArbitrationTab(t, manager, connected)

	var wg sync.WaitGroup
	for index := 0; index < 4; index++ {
		guest := fmt.Sprintf("preview-%d", index)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 25; round++ {
				_ = manager.WriteGuest(ctx, tab.ID, guest, []byte("x"), 5*time.Millisecond)
				_ = manager.ReleaseGuest(tab.ID, guest)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := 0; round < 25; round++ {
			_, _ = manager.Claim(tab.ID, "owner-client")
			_, _ = manager.Release(tab.ID, "owner-client")
		}
	}()
	wg.Wait()
}
