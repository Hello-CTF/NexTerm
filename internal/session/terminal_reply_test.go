package session

import (
	"context"
	"testing"
)

func TestIsTerminalReply(t *testing.T) {
	replies := []string{
		"\x1b[?1;2c",
		"\x1b[>0;276;0c",
		"\x1b[0n",
		"\x1b[12;40R",
		"\x1b[?12;40R",
		"\x1b[8;24;80t",
		"\x1b[?2026;2$y",
		"\x1b[?1u",
		"\x1bP>|xterm.js(5.5.0)\x1b\\",
		"\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\",
		"\x1b]4;1;rgb:cd/00/00\x07",
		"\x1b[I",
		"\x1b[O",
		"\x1b[?997;1n",
		// A desktop write merges replies queued meanwhile.
		"\x1b[?1u\x1b[?62;22c\x1b[3;1R",
	}
	for _, reply := range replies {
		if !isTerminalReply([]byte(reply)) {
			t.Errorf("isTerminalReply(%q) = false, want true", reply)
		}
	}
	typed := []string{
		"",
		"a",
		"ls\r",
		"\x1b[A",
		"\x1b",
		"\x1b[<0;10;5M",
		"\x1b[M #!",
		"\x1b[200~text\x1b[201~",
		// A reply merged with a key is typing.
		"\x1b[0nq",
		"q\x1b[0n",
	}
	for _, input := range typed {
		if isTerminalReply([]byte(input)) {
			t.Errorf("isTerminalReply(%q) = true, want false", input)
		}
	}
}

// A view nobody uses still answers the queries of programs started from
// another device. Its replies must not take the size back from the device
// that asked for it, while its typing still does after the handoff hold.
func TestTerminalRepliesDoNotTakeTheSize(t *testing.T) {
	events := make(chan ControlEvent, 256)
	clock := newSessionTestClock()
	manager, _, _, session := openGridTestSessionAt(t, events, clock.Now)
	ctx := context.Background()
	tab := openTestTab(t, manager, session, "desktop", "desktop-1")
	if _, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "phone", ChannelID: "phone-1"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(ctx, tab.ID, "desktop", []byte("d")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(sizeHandoffHold)
	if err := manager.TakeSize(tab.ID, "phone"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resize(ctx, tab.ID, "phone", 50, 30); err != nil {
		t.Fatal(err)
	}
	drainControlEvents(events)

	for _, reply := range []string{"\x1b[?1;2c", "\x1b[12;40R\x1b[I", "\x1b]11;rgb:0000/0000/0000\x1b\\"} {
		clock.Advance(sizeHandoffHold)
		if err := manager.Write(ctx, tab.ID, "desktop", []byte(reply)); err != nil {
			t.Fatal(err)
		}
		if owner := tab.Info().SizeOwner; owner != "phone" {
			t.Fatalf("reply %q moved the size to %q", reply, owner)
		}
	}
	if info := tab.Info(); info.Cols != 50 || info.Rows != 30 {
		t.Fatalf("size after the desktop's replies = %+v, want the phone's 50x30", info)
	}
	for event := range pendingControlEvents(events) {
		if event.SizeOwner != "phone" {
			t.Fatalf("a reply published a control event for %q", event.SizeOwner)
		}
	}

	// Typing on the desktop still takes the size after the hold.
	clock.Advance(sizeHandoffHold)
	if err := manager.Write(ctx, tab.ID, "desktop", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "desktop" {
		t.Fatalf("size owner after the desktop typed = %q, want desktop", owner)
	}
}

// The same holds for a durable tab another device sized through its own
// connection to the helper: this manager's only view answers every query,
// and that must not resize the PTY back to its size.
func TestTerminalRepliesKeepTheRemoteSize(t *testing.T) {
	events := make(chan ControlEvent, 256)
	clock := newSessionTestClock()
	provider := &sizePushingProvider{fakeDurableProvider: newFakeDurableProvider()}
	manager := NewManager(Config{
		Connector:       newFakeConnector(),
		Terminals:       newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider},
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				events <- event.Payload.(ControlEvent)
			}
			return nil
		}),
		Now: clock.Now,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "shared-helper", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "desktop", ChannelID: "desktop-1", Cols: 160, Rows: 50,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider.push(50, 30)
	waitControlEvent(t, events, func(event ControlEvent) bool { return event.SizeOwner == remoteSizeOwner })

	clock.Advance(sizeHandoffHold)
	if err := manager.Write(ctx, tab.ID, "desktop", []byte("\x1b[?62;22c")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != remoteSizeOwner {
		t.Fatalf("a reply took the remote size for %q", owner)
	}
	if err := manager.Resize(ctx, tab.ID, "desktop", 160, 50); err != nil {
		t.Fatal(err)
	}
	if info := tab.Info(); info.Cols != 50 || info.Rows != 30 {
		t.Fatalf("size after the reply = %+v, want the other device's 50x30", info)
	}
	if err := manager.Write(ctx, tab.ID, "desktop", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if owner := tab.Info().SizeOwner; owner != "desktop" {
		t.Fatalf("size owner after typing = %q, want desktop", owner)
	}
}
