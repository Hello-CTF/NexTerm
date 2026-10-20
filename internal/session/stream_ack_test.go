package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// An out-of-order ACK must never regress the retention floor: the floor is
// monotonic per channel, so a stale report is ignored.
func TestStreamAckFloorIsMonotonic(t *testing.T) {
	manager, _, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", 64<<20); err != nil {
		t.Fatal(err)
	}
	// A newer request completes first, then an older one lands late: the floor
	// must stay at the higher offset.
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", 16<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != 64<<20 {
		t.Fatalf("floor = %d, want 64 MiB (monotonic, stale ACK ignored)", got)
	}
}

// One client with two channels on the same tab: each channel's floor is set
// independently from its own ACK, not from the first map match.
func TestStreamAckDualChannelIndependentOffsets(t *testing.T) {
	manager, _, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", 32<<20); err != nil {
		t.Fatal(err)
	}
	if err := manager.StreamAckFor(context.Background(), info.ID, "b-1", "client-a", 16<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != 32<<20 {
		t.Fatalf("channel a-1 floor = %d, want 32 MiB", got)
	}
	if got := tab.subscribers["b-1"].episodeFloor; got != 16<<20 {
		t.Fatalf("channel b-1 floor = %d, want 16 MiB", got)
	}
	if got := tab.recovery.floor; got != 16<<20 {
		t.Fatalf("ring floor = %d, want the minimum 16 MiB", got)
	}
}

// The normal Write path passes the ACK-shaped literal straight through to the
// PTY: it is terminal input, not a control frame.
func TestStreamAckLiteralInputPassesThrough(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	literal := []byte(`{"type":"nexterm.stream-ack","offset":1048576}`)
	if err := manager.WriteFor(context.Background(), info.ID, "client-a", literal); err != nil {
		t.Fatal(err)
	}
	if got := connector.transport(0).channel(0).written(); !bytes.Equal(got, literal) {
		t.Fatalf("PTY input = %q, want the literal bytes passed through", got)
	}
}

// A non-controller attached observer cannot modify the retention floor: the
// ACK-shaped write is rejected with ErrNotController, and the floor is
// untouched.
func TestStreamAckObserverForbidden(t *testing.T) {
	manager, _, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.WriteFor(context.Background(), info.ID, "client-b", []byte(`{"type":"nexterm.stream-ack","offset":1048576}`))
	if !errors.Is(err, ErrNotController) {
		t.Fatalf("observer write = %v, want ErrNotController", err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor = %d, want -1 (observer cannot set it)", got)
	}
}

// The typed ACK control path never invokes the AI user-input hook, while a
// normal Write does: terminal output must not pause a running AI task.
func TestStreamAckDoesNotTriggerUserInputHook(t *testing.T) {
	manager, _, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	hookCalls := 0
	manager.SetUserInputHook(func(string) { hookCalls++ })

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", 1<<20); err != nil {
		t.Fatal(err)
	}
	if hookCalls != 0 {
		t.Fatalf("user-input hook calls = %d, want 0 for a typed ACK", hookCalls)
	}
	if err := manager.WriteFor(context.Background(), info.ID, "client-a", []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if hookCalls != 1 {
		t.Fatalf("user-input hook calls = %d, want 1 for a normal Write", hookCalls)
	}
}
