package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// attachmentIdFor returns the current incarnation of the channel's attachment.
func attachmentIdFor(t *testing.T, manager *Manager, tabID, channelID string) uint64 {
	t.Helper()
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	defer tab.mu.Unlock()
	subscriber, ok := tab.subscribers[channelID]
	if !ok {
		t.Fatalf("channel %s not attached", channelID)
	}
	return subscriber.attachmentId
}

// An out-of-order ACK must never regress the retention floor: the floor is
// monotonic per channel, so a stale report is ignored.
func TestStreamAckFloorIsMonotonic(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
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
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	drainAttachHeader(t, receiver)
	for i := 0; i < 128; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("chunk %d mismatch", i)
		}
	}

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 8<<20); err != nil {
		t.Fatal(err)
	}
	// A newer request completes first, then an older one lands late: the floor
	// must stay at the higher offset.
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 4<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != 8<<20 {
		t.Fatalf("floor = %d, want 8 MiB (monotonic, stale ACK ignored)", got)
	}
}

// One client with two channels on the same tab: each channel's floor is set
// independently from its own ACK, not from the first map match.
func TestStreamAckDualChannelIndependentOffsets(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
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
	channel := connector.transport(0).channel(0)
	receiverA := bindTestReceiver(t, manager, "a-1")
	receiverB := bindTestReceiver(t, manager, "b-1")
	drainAttachHeader(t, receiverA)
	drainAttachHeader(t, receiverB)
	for i := 0; i < 128; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiverA, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("channel A chunk %d mismatch", i)
		}
		if got := readFramesUntil(t, receiverB, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("channel B chunk %d mismatch", i)
		}
	}

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := manager.StreamAckFor(context.Background(), info.ID, "b-1", "client-a", attachmentIdFor(t, manager, info.ID, "b-1"), 4<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != 8<<20 {
		t.Fatalf("channel a-1 floor = %d, want 8 MiB", got)
	}
	if got := tab.subscribers["b-1"].episodeFloor; got != 4<<20 {
		t.Fatalf("channel b-1 floor = %d, want 4 MiB", got)
	}
	if got := tab.recovery.floor; got != 4<<20 {
		t.Fatalf("ring floor = %d, want the minimum 4 MiB", got)
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

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 1<<20); err != nil {
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

// A controller cannot ACK another client's channel: the ACK is bound to the
// attached channel, and a cross-client report is rejected with the floor
// untouched.
func TestStreamAckCrossClientChannelForbidden(t *testing.T) {
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
	err = manager.StreamAckFor(context.Background(), info.ID, "b-1", "client-a", attachmentIdFor(t, manager, info.ID, "b-1"), 8<<20)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-client ACK = %v, want ErrForbidden", err)
	}
	if got := tab.subscribers["b-1"].episodeFloor; got != -1 {
		t.Fatalf("channel b-1 floor = %d, want -1 (cross-client rejected)", got)
	}
}

// An ACK for a channel that is not attached to the tab is rejected.
func TestStreamAckUnknownChannelForbidden(t *testing.T) {
	manager, _, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	err = manager.StreamAckFor(context.Background(), info.ID, "nope", "client-a", 0, 8<<20)
	if !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("unknown-channel ACK = %v, want ErrTabNotFound", err)
	}
}

// A legitimate attached observer can ACK its own channel and establish its
// floor: the retention guarantee does not depend on being the tab controller.
func TestStreamAckObserverOwnChannelFloor(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
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
	channel := connector.transport(0).channel(0)
	receiverA := bindTestReceiver(t, manager, "a-1")
	receiverB := bindTestReceiver(t, manager, "b-1")
	drainAttachHeader(t, receiverA)
	drainAttachHeader(t, receiverB)
	var fed []byte
	for i := 0; i < 256; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
		fed = append(fed, chunk...)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiverA, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("channel A chunk %d mismatch", i)
		}
		if got := readFramesUntil(t, receiverB, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("channel B chunk %d mismatch", i)
		}
	}
	const acceptedB = 16 << 20
	if err := manager.StreamAckFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB); err != nil {
		t.Fatalf("observer own-channel ACK = %v, want nil", err)
	}
	if got := tab.subscribers["b-1"].episodeFloor; got != acceptedB {
		t.Fatalf("channel b-1 floor = %d, want 16 MiB (observer's own floor)", got)
	}
}

// A negative accepted offset is rejected and never closes the floor through
// the ACK path.
func TestStreamAckNegativeRejected(t *testing.T) {
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
	err = manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), -1)
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("negative ACK = %v, want ErrInvalidOptions", err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor = %d, want -1 (negative ACK never applied)", got)
	}
}

// A future accepted offset is clamped to the channel's flushed offset.
func TestStreamAckFutureClampedToFlushed(t *testing.T) {
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
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 64<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != 0 {
		t.Fatalf("floor = %d, want 0 (clamped to flushedOffset 0)", got)
	}
}

// RecoverChannelFor rejects negative, future, and overflowing offsets instead
// of treating them as a successful empty recovery.
func TestRecoverChannelForRejectsOutOfRangeOffsets(t *testing.T) {
	manager, _, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RecoverChannelFor(context.Background(), "a-1", -1<<63); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("recover MinInt64 = %v, want ErrInvalidOptions", err)
	}
	if _, err := manager.RecoverChannelFor(context.Background(), "a-1", -1); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("recover(-1) = %v, want ErrInvalidOptions", err)
	}
	if _, err := manager.RecoverChannelFor(context.Background(), "a-1", 1<<62); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("recover(future) = %v, want ErrInvalidOptions", err)
	}
}

// A late stale ACK after a resume cannot reopen the old floor: the
// subscriber's persistent high-water makes it a no-op.
func TestStreamAckLateStaleACKIgnored(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
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
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	drainAttachHeader(t, receiver)
	for i := 0; i < 128; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("chunk %d mismatch", i)
		}
	}
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := manager.ResumeChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor after resume = %d, want -1 (cleared)", got)
	}
	// A late stale ACK from before the resume must not reopen the old floor.
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 4<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor after late stale ACK = %d, want -1 (high-water holds)", got)
	}
	if got := tab.subscribers["a-1"].floorHighWater; got != 8<<20 {
		t.Fatalf("floorHighWater = %d, want 8 MiB (unchanged)", got)
	}
}

// A late ACK from a previous incarnation of the same channel is rejected: the
// attachmentId is bound to the current incarnation, so an old report can
// never reopen the floor or suppress a new lower-but-real offset.
func TestStreamAckReattachOldIncarnationRejected(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
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
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	drainAttachHeader(t, receiver)
	for i := 0; i < 128; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, len(chunk)); !bytes.Equal(got, chunk) {
			t.Fatalf("chunk %d mismatch", i)
		}
	}
	oldAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")

	// Re-attach the same channel: a new incarnation takes over.
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	newAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	if newAttachmentId == oldAttachmentId {
		t.Fatal("re-attach did not create a new incarnation")
	}

	// The old incarnation's ACK is rejected, whether it arrives before or
	// after a new-incarnation ACK.
	err = manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("old-incarnation ACK = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor after old-incarnation ACK = %d, want -1 (rejected)", got)
	}
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, 4<<20); err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != 4<<20 {
		t.Fatalf("floor after new-incarnation ACK = %d, want 4 MiB (accepted)", got)
	}
	err = manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("late old-incarnation ACK = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != 4<<20 {
		t.Fatalf("floor after late old-incarnation ACK = %d, want 4 MiB (unchanged)", got)
	}
}
