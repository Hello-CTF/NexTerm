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

// TestRemoteSessionFeedAndSpectate pins the shell's core data plane: pushed output enters both the screen model
// (snapshot source) and Spectator subscriptions, which receive snapshot frames + binary delta frames identical to a real terminal.
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

// TestRemoteSessionRejectsNonRemote: remote channels only act on shells; normal terminal tabs and unknown
// tabs are always rejected, preventing accidental writes to real terminals.
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

// TestRemoteSessionEndKeepsSnapshot: end has the same semantics as a real process exit - the tab is kept,
// exited is set, new subscribers still get a snapshot containing the existing output, but feeding and feed connections are no longer accepted.
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
	// Repeated end is idempotent.
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

// TestRemoteFeedExclusive: only one active feed connection is allowed per shell; reattach works after release.
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
	// Release fallback: a repeated detach must not push the count negative and block later attaches.
	manager.DetachRemoteFeed(again)
	if _, err := manager.AttachRemoteFeed(info.ID); err != nil {
		t.Fatalf("attach after double detach: %v", err)
	}
}

// TestRemoteSessionLimit: per-account active shell limit; others are unaffected; CloseRemote frees the slot.
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

// TestRemoteIdleSweep pins idle reaping: a shell with no feed, no watchers, and timed-out activity is reaped
// (tab and session removed together); an active feed connection or subscribed watchers suspend reaping; disconnect resets the timer.
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

	// With a feed connection attached, the shell is not reaped even if the activity time has passed.
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

	// Disconnect resets the timer: an immediate sweep does not reap.
	manager.DetachRemoteFeed(feed)
	if err := manager.SweepIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("just-detached shell reaped: %v", err)
	}

	// With subscribed watchers the shell is likewise not reaped; once the watcher leaves and activity has timed out, it is reaped.
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

// TestRemoteCloseEndsSpectators: explicit termination (CloseRemote) removes the shell and closes subscriber channels,
// consistent with a real tab close (the viewer side sees the stream end).
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

// TestRemoteResize: resize syncs the screen model and tab size, and the snapshot reflects the new size.
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
