package session

import (
	"bytes"
	"context"
	"testing"
)

func TestChannelDropRebindReattachReplaysScrollback(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")

	before := []byte("nx-marker-before-drop")
	if err := channel.emit(before); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, receiver, before)

	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Subscribers; got != 0 {
		t.Fatalf("subscribers after drop = %d, want 0", got)
	}
	if got := tab.Info().Controller; got != "" {
		t.Fatalf("controller after drop = %q, want released", got)
	}

	during := []byte("nx-output-during-drop")
	if err := channel.emit(during); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(terminals.terminal(tab.ID).bytes(), during) })

	rebound := bindTestReceiver(t, manager, "a-1")
	info, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1", ReplayBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if info.Subscribers != 1 || info.Controller != "client-a" {
		t.Fatalf("reattach info = %+v", info)
	}
	assertFrameData(t, rebound, replayClear)
	replay := receiveTestFrame(t, rebound)
	if !bytes.Contains(replay.Data, before) || !bytes.Contains(replay.Data, during) {
		t.Fatalf("replay lost scrollback: %q", replay.Data)
	}

	after := []byte("nx-live-after-reattach")
	if err := channel.emit(after); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, rebound, after)
}

func TestChannelRebindBeforeOldReceiverCloseKeepsNewBinding(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	old := bindTestReceiver(t, manager, "a-1")

	before := []byte("nx-marker-half-open")
	if err := channel.emit(before); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, old, before)

	rebound := bindTestReceiver(t, manager, "a-1")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Subscribers; got != 1 {
		t.Fatalf("subscribers after stale close = %d, want 1", got)
	}

	info, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1", ReplayBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if info.Subscribers != 1 || info.Controller != "client-a" {
		t.Fatalf("reattach info = %+v", info)
	}
	assertFrameData(t, rebound, replayClear)
	replay := receiveTestFrame(t, rebound)
	if !bytes.Contains(replay.Data, before) {
		t.Fatalf("replay lost scrollback: %q", replay.Data)
	}

	after := []byte("nx-live-after-half-open")
	if err := channel.emit(after); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, rebound, after)
}
