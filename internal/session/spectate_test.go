package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// TestAttachSpectatorSnapshotThenLive 钉住只读订阅的核心契约: 首帧是当前屏幕
// 快照 (JSON 文本帧), 之后只有实时输出 (二进制帧), 不含滚动历史, 且快照与
// 实时输出之间无缺口 (feed 锁内完成快照+订阅)。
func TestAttachSpectatorSnapshotThenLive(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: terminalFactory{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "spect-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	channel := connector.transport(0).channel(0)

	if err := channel.emit([]byte("alpha-line\r\nprompt$ ")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() >= uint64(len("alpha-line\r\nprompt$ ")) })

	info, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "viewer-1", ChannelID: "spect-1"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Controller != "owner-client" {
		t.Fatalf("spectator stole control: controller = %q", info.Controller)
	}

	receiver, err := manager.Hub().Bind("spect-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	next := func() hub.Frame {
		t.Helper()
		frameCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		frame, err := receiver.Next(frameCtx)
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
		return frame
	}

	first := next()
	if first.Kind != hub.FrameJSON {
		t.Fatalf("first frame kind = %v, want JSON snapshot", first.Kind)
	}
	var snapshot spectateSnapshot
	if err := json.Unmarshal(first.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != SpectateSnapshotType || snapshot.Cols != 80 || snapshot.Rows != 24 {
		t.Fatalf("snapshot frame = %+v", snapshot)
	}
	joined := strings.Join(snapshot.Lines, "\n")
	if !strings.Contains(joined, "alpha-line") || !strings.Contains(joined, "prompt$") {
		t.Fatalf("snapshot lines = %q", joined)
	}

	// 订阅后的实时输出以二进制帧到达, 内容与顺序与终端输出一致。
	if err := channel.emit([]byte("beta-line\r\n")); err != nil {
		t.Fatal(err)
	}
	live := next()
	if live.Kind != hub.FrameBinary || string(live.Data) != "beta-line\r\n" {
		t.Fatalf("live frame = kind %v %q", live.Kind, live.Data)
	}
}

// TestAttachSpectatorNoControlNoInput 钉住只读边界: 观看者不取得控制权 (无
// 控制者时也不占), 以其身份写输入一律 ErrNotController。
func TestAttachSpectatorNoControlNoInput(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "spect-ro-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	// 摘掉 owner 通道, 让标签页没有控制者。
	if err := manager.DetachChannel("owner-1"); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Controller != "" {
		t.Fatalf("precondition: controller = %q, want empty", info.Controller)
	}

	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "viewer-1", ChannelID: "spect-1"}); err != nil {
		t.Fatal(err)
	}
	info := tab.Info()
	if info.Controller != "" {
		t.Fatalf("spectator claimed control: %q", info.Controller)
	}
	if info.Viewers != 1 || info.Subscribers != 1 {
		t.Fatalf("spectator accounting = viewers %d subscribers %d", info.Viewers, info.Subscribers)
	}
	if err := manager.Write(ctx, tab.ID, "viewer-1", []byte("echo nope\n")); err == nil {
		t.Fatal("spectator write accepted")
	}

	if err := manager.DetachChannel("spect-1"); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Subscribers; got != 0 {
		t.Fatalf("detached spectator left %d subscribers", got)
	}
}

// TestAttachSpectatorValidates 钉住参数与状态校验: 缺通道 ID、标签页不存在、
// 通道被他页占用都拒绝。
func TestAttachSpectatorValidates(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "spect-val-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")
	other := openTestTab(t, manager, connected, "owner-client", "owner-2")

	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "viewer-1"}); err == nil {
		t.Fatal("empty channel id accepted")
	}
	if _, err := manager.AttachSpectator(ctx, "missing-tab", SpectateOptions{ClientID: "viewer-1", ChannelID: "spect-1"}); err == nil {
		t.Fatal("missing tab accepted")
	}
	if _, err := manager.AttachSpectator(ctx, other.ID, SpectateOptions{ClientID: "viewer-1", ChannelID: "owner-1"}); err == nil {
		t.Fatal("channel owned by another tab accepted")
	}
}

func TestTabOwner(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
	})
	t.Cleanup(func() { _ = manager.Close() })
	userCtx := ipc.WithUserID(context.Background(), "user-42")
	connected, err := manager.Connect(userCtx, Asset{ID: "owner-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")

	owner, err := manager.TabOwner(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner != "user-42" {
		t.Fatalf("TabOwner = %q, want user-42", owner)
	}
	if _, err := manager.TabOwner("missing-tab"); err == nil {
		t.Fatal("missing tab accepted")
	}
}
