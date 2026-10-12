package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

func newRemoteManager(t *testing.T, mutate ...func(*Config)) *Manager {
	t.Helper()
	config := Config{Terminals: terminalFactory{}}
	for _, apply := range mutate {
		apply(&config)
	}
	manager := NewManager(config)
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func registerRemote(t *testing.T, manager *Manager, owner string) TabInfo {
	t.Helper()
	info, err := manager.RegisterRemoteSession(context.Background(), RemoteSessionOptions{
		OwnerID: owner, Title: "demo", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// TestRemoteSessionFeedAndSpectate 钉住壳的核心数据面: 推入的输出既进屏幕模型
// (快照源), 也使 Spectator 订阅拿到与真实终端一致的 快照帧 + 二进制增量帧。
func TestRemoteSessionFeedAndSpectate(t *testing.T) {
	manager := newRemoteManager(t)
	ctx := context.Background()
	info := registerRemote(t, manager, "user-a")
	if info.Cols != 80 || info.Rows != 24 {
		t.Fatalf("info = %+v", info)
	}
	owner, err := manager.TabOwner(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner != "user-a" {
		t.Fatalf("TabOwner = %q, want user-a", owner)
	}

	if err := manager.FeedRemote(ctx, info.ID, []byte("remote-line\r\nprompt$ ")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.TerminalSnapshot(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(snapshot.Lines, "\n"); !strings.Contains(joined, "remote-line") {
		t.Fatalf("snapshot lines = %q", joined)
	}

	if _, err := manager.AttachSpectator(ctx, info.ID, SpectateOptions{ClientID: "viewer", ChannelID: "remote-spect-1"}); err != nil {
		t.Fatal(err)
	}
	receiver, err := manager.Hub().Bind("remote-spect-1")
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
	var frame spectateSnapshot
	if err := json.Unmarshal(first.Data, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != SpectateSnapshotType {
		t.Fatalf("snapshot frame = %+v", frame)
	}
	if joined := strings.Join(frame.Lines, "\n"); !strings.Contains(joined, "remote-line") {
		t.Fatalf("snapshot lines = %q", joined)
	}

	if err := manager.FeedRemote(ctx, info.ID, []byte("live-42\r\n")); err != nil {
		t.Fatal(err)
	}
	live := next()
	if live.Kind != hub.FrameBinary || string(live.Data) != "live-42\r\n" {
		t.Fatalf("live frame = kind %v %q", live.Kind, live.Data)
	}
}

// TestRemoteSessionRejectsNonRemote: 远程通道只作用于壳; 普通终端标签页与未知
// 标签页一律拒绝, 防止误写真实终端。
func TestRemoteSessionRejectsNonRemote(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "plain-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "owner-client", "owner-1")

	if err := manager.FeedRemote(ctx, tab.ID, []byte("x")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("FeedRemote on plain tab = %v, want ErrUnsupported", err)
	}
	if _, err := manager.RemoteSessionInfo(tab.ID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("RemoteSessionInfo on plain tab = %v, want ErrUnsupported", err)
	}
	if _, err := manager.AttachRemoteFeed(tab.ID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("AttachRemoteFeed on plain tab = %v, want ErrUnsupported", err)
	}
	if err := manager.CloseRemote(tab.ID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CloseRemote on plain tab = %v, want ErrUnsupported", err)
	}
	if err := manager.FeedRemote(ctx, "missing", []byte("x")); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("FeedRemote on missing tab = %v, want ErrTabNotFound", err)
	}
}

// TestRemoteSessionEndKeepsSnapshot: end 与真实进程退出同语义 — 标签页保留、
// exited 置位、新订阅者仍拿到含既有输出的快照, 但不再接受推流与推流连接。
func TestRemoteSessionEndKeepsSnapshot(t *testing.T) {
	manager := newRemoteManager(t)
	ctx := context.Background()
	info := registerRemote(t, manager, "user-a")
	if err := manager.FeedRemote(ctx, info.ID, []byte("before-end\r\n")); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := manager.EndRemote(info.ID, &code); err != nil {
		t.Fatal(err)
	}
	// 重复 end 幂等。
	if err := manager.EndRemote(info.ID, nil); err != nil {
		t.Fatal(err)
	}
	ended, err := manager.RemoteSessionInfo(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ended.Exited {
		t.Fatal("tab not marked exited after EndRemote")
	}
	if err := manager.FeedRemote(ctx, info.ID, []byte("after\r\n")); !errors.Is(err, ErrTabClosed) {
		t.Fatalf("FeedRemote after end = %v, want ErrTabClosed", err)
	}
	if _, err := manager.AttachRemoteFeed(info.ID); !errors.Is(err, ErrTabClosed) {
		t.Fatalf("AttachRemoteFeed after end = %v, want ErrTabClosed", err)
	}

	if _, err := manager.AttachSpectator(ctx, info.ID, SpectateOptions{ClientID: "viewer", ChannelID: "remote-end-spect"}); err != nil {
		t.Fatal(err)
	}
	receiver, err := manager.Hub().Bind("remote-end-spect")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	frameCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	first, err := receiver.Next(frameCtx)
	if err != nil {
		t.Fatal(err)
	}
	var frame spectateSnapshot
	if err := json.Unmarshal(first.Data, &frame); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(frame.Lines, "\n"); !strings.Contains(joined, "before-end") {
		t.Fatalf("snapshot after end = %q", joined)
	}
}

// TestRemoteFeedExclusive: 同一壳只允许一条活跃推流连接; 释放后可重连续推。
func TestRemoteFeedExclusive(t *testing.T) {
	manager := newRemoteManager(t)
	info := registerRemote(t, manager, "user-a")

	session, err := manager.AttachRemoteFeed(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachRemoteFeed(info.ID); !errors.Is(err, ErrRemoteFeedAttached) {
		t.Fatalf("second attach = %v, want ErrRemoteFeedAttached", err)
	}
	manager.DetachRemoteFeed(session)
	again, err := manager.AttachRemoteFeed(info.ID)
	if err != nil {
		t.Fatalf("reattach after detach: %v", err)
	}
	manager.DetachRemoteFeed(again)
	// 释放兜底: 重复 detach 不得把计数压成负数阻断后续 attach。
	manager.DetachRemoteFeed(again)
	if _, err := manager.AttachRemoteFeed(info.ID); err != nil {
		t.Fatalf("attach after double detach: %v", err)
	}
}

// TestRemoteSessionLimit: 单账号活跃壳上限; 他人不受影响; CloseRemote 释放名额。
func TestRemoteSessionLimit(t *testing.T) {
	manager := newRemoteManager(t)
	first := registerRemote(t, manager, "user-a")
	for i := 1; i < MaxRemoteSessionsPerUser; i++ {
		registerRemote(t, manager, "user-a")
	}
	if _, err := manager.RegisterRemoteSession(context.Background(), RemoteSessionOptions{OwnerID: "user-a"}); !errors.Is(err, ErrRemoteLimit) {
		t.Fatalf("over-limit register = %v, want ErrRemoteLimit", err)
	}
	if _, err := manager.RegisterRemoteSession(context.Background(), RemoteSessionOptions{OwnerID: "user-b"}); err != nil {
		t.Fatalf("other user register: %v", err)
	}
	if err := manager.CloseRemote(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RegisterRemoteSession(context.Background(), RemoteSessionOptions{OwnerID: "user-a"}); err != nil {
		t.Fatalf("register after close: %v", err)
	}
}

// TestRemoteIdleSweep 钉住空闲回收: 无推流、无观看且活动超时的壳被收割
// (标签页与会话一并移除); 活跃推流连接或订阅观看者都挂起回收; 断连重置计时。
func TestRemoteIdleSweep(t *testing.T) {
	manager := newRemoteManager(t, func(config *Config) { config.IdleTimeout = 30 * time.Millisecond })
	ctx := context.Background()
	info := registerRemote(t, manager, "user-a")
	session, err := manager.Session(info.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	stale := func() {
		session.mu.Lock()
		session.remoteActivity = time.Now().Add(-time.Hour)
		session.mu.Unlock()
	}

	// 有推流连接时即使活动时刻已过也不收割。
	stale()
	feed, err := manager.AttachRemoteFeed(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale()
	if err := manager.SweepIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("attached feed shell reaped: %v", err)
	}

	// 断连重置计时: 立刻扫不收割。
	manager.DetachRemoteFeed(feed)
	if err := manager.SweepIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("just-detached shell reaped: %v", err)
	}

	// 有订阅观看者时同样不收割; 观看者离开且活动已超时即收割。
	if _, err := manager.AttachSpectator(ctx, info.ID, SpectateOptions{ClientID: "viewer", ChannelID: "remote-idle-spect"}); err != nil {
		t.Fatal(err)
	}
	stale()
	if err := manager.SweepIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("watched shell reaped: %v", err)
	}
	if err := manager.DetachChannel("remote-idle-spect"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SweepIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("idle shell tab = %v, want ErrTabNotFound", err)
	}
	if _, err := manager.Session(info.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("idle shell session = %v, want ErrSessionNotFound", err)
	}
}

// TestRemoteCloseEndsSpectators: 显式终结 (CloseRemote) 移除壳并关闭订阅通道,
// 与真实标签页关闭一致 (viewer 侧表现为流终止)。
func TestRemoteCloseEndsSpectators(t *testing.T) {
	manager := newRemoteManager(t)
	ctx := context.Background()
	info := registerRemote(t, manager, "user-a")
	if _, err := manager.AttachSpectator(ctx, info.ID, SpectateOptions{ClientID: "viewer", ChannelID: "remote-close-spect"}); err != nil {
		t.Fatal(err)
	}
	receiver, err := manager.Hub().Bind("remote-close-spect")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })

	if err := manager.CloseRemote(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("closed shell tab = %v, want ErrTabNotFound", err)
	}
	frameCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := receiver.Next(frameCtx); !errors.Is(err, hub.ErrClosed) {
		t.Fatalf("spectator next after close = %v, want hub.ErrClosed", err)
	}
	if err := manager.CloseRemote(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("second CloseRemote = %v, want ErrTabNotFound", err)
	}
}

// TestRemoteResize: resize 同步屏幕模型与标签页尺寸, 快照随之反映新尺寸。
func TestRemoteResize(t *testing.T) {
	manager := newRemoteManager(t)
	ctx := context.Background()
	info := registerRemote(t, manager, "user-a")
	if err := manager.ResizeRemote(ctx, info.ID, 120, 40); err != nil {
		t.Fatal(err)
	}
	updated, err := manager.RemoteSessionInfo(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Cols != 120 || updated.Rows != 40 {
		t.Fatalf("info after resize = %+v", updated)
	}
	snapshot, err := manager.TerminalSnapshot(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Cols != 120 || snapshot.Rows != 40 {
		t.Fatalf("snapshot after resize = %+v", snapshot)
	}
	if err := manager.ResizeRemote(ctx, info.ID, 0, 40); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("invalid resize = %v, want ErrInvalidSize", err)
	}
}
