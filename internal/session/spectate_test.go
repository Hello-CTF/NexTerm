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

// TestAttachSpectatorSnapshotThenLive pins the read-only subscription's core contract: the first frame is the current
// screen snapshot (JSON text frame), afterwards only live output (binary frames), no scrollback, and no gap between
// the snapshot and live output (snapshot + subscription happen inside the feed lock).
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

	// Live output after subscribing arrives as binary frames, matching the terminal output in content and order.
	if err := channel.emit([]byte("beta-line\r\n")); err != nil {
		t.Fatal(err)
	}
	live := next()
	if live.Kind != hub.FrameBinary || string(live.Data) != "beta-line\r\n" {
		t.Fatalf("live frame = kind %v %q", live.Kind, live.Data)
	}
}

// TestAttachSpectatorNoControlNoInput pins the read-only boundary: the watcher does not take control (not even when
// no one holds it), and writing input as the watcher always yields ErrNotController.
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
	// Detach the owner channel so the tab has no controller.
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

// TestAttachSpectatorValidates pins parameter and state validation: missing channel ID, nonexistent tab,
// and a channel already used by another tab are all rejected.
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
