package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

func readFramesUntil(t *testing.T, receiver *hub.Receiver, want int) []byte {
	t.Helper()
	var got []byte
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < want {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		frame, err := receiver.Next(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read %d of %d bytes: %v", len(got), want, err)
		}
		got = append(got, frame.Data...)
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func expectNoFrame(t *testing.T, receiver *hub.Receiver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if frame, err := receiver.Next(ctx); err == nil {
		t.Fatalf("unexpected frame while paused: %q", frame.Data)
	}
}

func drainAttachHeader(t *testing.T, receiver *hub.Receiver) {
	t.Helper()
	frame := receiveTestFrame(t, receiver)
	if !bytes.Equal(frame.Data, []byte("\x1b[2J\x1b[3J\x1b[H")) {
		t.Fatalf("attach header = %q", frame.Data)
	}
}

func openOpenTestSession(t *testing.T, kind string) (*Manager, *fakeConnector, *fakeTerminalFactory, *Session) {
	t.Helper()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{
		Connector: connector, Terminals: terminals, ReconnectMax: 2,
		ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-" + kind, Name: kind, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connector, terminals, session
}

func TestRecoverChannelForRedeliversRefusedSuffix(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	drainAttachHeader(t, receiver)

	if err := channel.emit([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "one" {
		t.Fatalf("first frame = %q", got)
	}
	if err := channel.emit([]byte("two")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "two" {
		t.Fatalf("second frame = %q", got)
	}

	// The client accepted only the first frame: recover the refused suffix.
	if err := manager.RecoverChannelFor(context.Background(), "a-1", 3); err != nil {
		t.Fatal(err)
	}
	// A synthetic anchor opens the replay, then the refused bytes arrive.
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "two" {
		t.Fatalf("recovered frame = %q", got)
	}
}

func TestRecoverChannelForCapacityBoundary(t *testing.T) {
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
	channel := connector.transport(0).channel(0)
	receiverA := bindTestReceiver(t, manager, "a-1")
	receiverB := bindTestReceiver(t, manager, "b-1")
	drainAttachHeader(t, receiverA)
	drainAttachHeader(t, receiverB)

	// Sustained output well past the ring's 32 MiB capacity: the refused
	// suffix per channel is still small enough to be redelivered whole.
	const chunkSize = 64 << 10
	const chunks = 600
	var fed []byte
	for i := 0; i < chunks; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		fed = append(fed, chunk...)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiverA, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel A chunk %d mismatch", i)
		}
		if got := readFramesUntil(t, receiverB, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel B chunk %d mismatch", i)
		}
	}

	// Independent accepted offsets: each channel recovers only its own
	// refused suffix, and the ring holds all of them.
	const acceptedA = 37<<20 + 512<<10
	const acceptedB = 36<<20 + 512<<10
	// a-1 accepted everything: nothing to recover, no anchor, no replay.
	if err := manager.RecoverChannelFor(context.Background(), "a-1", acceptedA); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiverA)
	// b-1 refused 1 MiB: anchor frame followed by the refused suffix.
	if err := manager.RecoverChannelFor(context.Background(), "b-1", acceptedB); err != nil {
		t.Fatal(err)
	}
	anchorB := receiveTestFrame(t, receiverB)
	if !bytes.Contains(anchorB.Data, []byte("recovery-")) {
		t.Fatalf("anchor B = %q", anchorB.Data)
	}
	suffixB := readFramesUntil(t, receiverB, len(fed)-acceptedB)
	if !bytes.Equal(suffixB, fed[acceptedB:]) {
		t.Fatalf("channel B suffix mismatch: got %d bytes want %d", len(suffixB), len(fed)-acceptedB)
	}
}

func TestRecoverChannelForExcludesPausedCoalescerFromReplay(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	drainAttachHeader(t, receiver)

	if err := channel.emit([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "one" {
		t.Fatalf("first frame = %q", got)
	}
	if err := channel.emit([]byte("two")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "two" {
		t.Fatalf("second frame = %q", got)
	}

	// The client accepted only "one" (absolute offset 3). Pause, then let one
	// more frame sit in the paused coalescer: it must not push the recovery
	// anchor forward, must not be replayed, and must arrive only via the
	// resume flush, so the full sequence stays contiguous without a hole.
	if err := manager.PauseChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("three")); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiver)

	if err := manager.RecoverChannelFor(context.Background(), "a-1", 3); err != nil {
		t.Fatal(err)
	}
	anchor := receiveTestFrame(t, receiver)
	var parsed streamAnchor
	if err := json.Unmarshal(anchor.Data, &parsed); err != nil {
		t.Fatalf("anchor frame = %q: %v", anchor.Data, err)
	}
	if parsed.StreamSeq != 3 {
		t.Fatalf("anchor streamSeq = %d, want the client's accepted offset 3", parsed.StreamSeq)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "two" {
		t.Fatalf("recovered frame = %q", got)
	}
	expectNoFrame(t, receiver)

	if err := manager.ResumeChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 5); string(got) != "three" {
		t.Fatalf("flushed frame = %q", got)
	}
}

func TestRecoverChannelForRefusedSuffixBeyondRingFails(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	drainAttachHeader(t, receiver)

	// Sustained output past the ring's 32 MiB capacity with a healthy
	// consumer: a client that then claims an accepted offset older than the
	// retention window must get an explicit overrun, never a short replay
	// that silently drops the evicted prefix.
	const chunkSize = 64 << 10
	const chunks = 600
	var fed []byte
	for i := 0; i < chunks; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		fed = append(fed, chunk...)
		if err := channel.emit(chunk); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("chunk %d mismatch", i)
		}
	}

	if err := manager.RecoverChannelFor(context.Background(), "a-1", 4<<20); !errors.Is(err, ErrRecoveryOverrun) {
		t.Fatalf("recover = %v, want ErrRecoveryOverrun", err)
	}
	expectNoFrame(t, receiver)

	// The boundary is exact: a refused suffix that still fits the window is
	// redelivered whole and byte-identical.
	const accepted = 37 << 20
	if err := manager.RecoverChannelFor(context.Background(), "a-1", accepted); err != nil {
		t.Fatal(err)
	}
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	suffix := readFramesUntil(t, receiver, len(fed)-accepted)
	if !bytes.Equal(suffix, fed[accepted:]) {
		t.Fatalf("suffix mismatch: got %d bytes want %d", len(suffix), len(fed)-accepted)
	}
}

func TestPauseChannelHoldsOutputAndResumeFlushesInOrder(t *testing.T) {
	manager, connector, _, session := openOpenTestSession(t, KindSSH)
	openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")

	if err := channel.emit([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "one" {
		t.Fatalf("first frame = %q", got)
	}

	if err := manager.PauseChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("two")); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiver)

	if err := manager.ResumeChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "two" {
		t.Fatalf("flushed frame = %q", got)
	}
	if err := channel.emit([]byte("three")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 5); string(got) != "three" {
		t.Fatalf("resumed frame = %q", got)
	}
}

func TestPauseChannelDoesNotDisturbOtherSubscribers(t *testing.T) {
	manager, connector, _, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	receiverA := bindTestReceiver(t, manager, "a-1")
	receiverB := bindTestReceiver(t, manager, "b-1")
	drainAttachHeader(t, receiverB)

	if err := manager.PauseChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverB, 1); string(got) != "x" {
		t.Fatalf("other subscriber frame = %q", got)
	}
	expectNoFrame(t, receiverA)

	if err := manager.ResumeChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverA, 1); string(got) != "x" {
		t.Fatalf("resumed subscriber frame = %q", got)
	}
}

func TestPauseChannelStallsPumpAndLosesNothing(t *testing.T) {
	manager, connector, _, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	receiverA := bindTestReceiver(t, manager, "a-1")
	receiverB := bindTestReceiver(t, manager, "b-1")
	drainAttachHeader(t, receiverB)

	if err := channel.emit([]byte("before")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverA, 6); string(got) != "before" {
		t.Fatalf("pre-pause frame A = %q", got)
	}
	gotB := readFramesUntil(t, receiverB, 6)
	if string(gotB) != "before" {
		t.Fatalf("pre-pause frame B = %q", gotB)
	}

	if err := manager.PauseChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	// Fill the paused coalescer, then the pipe: the pump must stall the PTY
	// read instead of growing memory without bound.
	stalled := false
	var injected []byte
	for i := 0; i < 300 && !stalled; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 8<<10)
		if err := channel.emit(chunk); err != nil {
			stalled = true
			break
		}
		injected = append(injected, chunk...)
	}
	if !stalled {
		t.Fatal("pump kept reading after the paused coalescer filled")
	}
	// The unpaused subscriber received the first chunk before the stall.
	gotB = append(gotB, readFramesUntil(t, receiverB, outputCoalesceMax)...)

	if err := manager.ResumeChannelFor(context.Background(), "a-1"); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverA, len(injected)); !bytes.Equal(got, injected) {
		t.Fatalf("resumed stream A mismatch: got %d bytes want %d", len(got), len(injected))
	}
	gotB = append(gotB, readFramesUntil(t, receiverB, len(injected)-outputCoalesceMax)...)
	if !bytes.Equal(gotB[6:], injected) {
		t.Fatalf("resumed stream B mismatch: got %d bytes want %d", len(gotB)-6, len(injected))
	}
}

func TestPauseChannelKeepsEphemeralTabAlive(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-local"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.PauseChannelFor(context.Background(), "a-local"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("ephemeral tab reaped by pause: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("PTY closed by pause: close count = %d", got)
	}
	if err := manager.ResumeChannelFor(context.Background(), "a-local"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("ephemeral tab gone after resume: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("PTY closed after resume: close count = %d", got)
	}
}

func TestPauseChannelRequiresOwnership(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	openTestTab(t, manager, session, "client-a", "a-1")

	if err := manager.PauseChannelFor(requestTestUser("owner-b"), "a-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other owner pause = %v, want ErrForbidden", err)
	}
	if err := manager.PauseChannelFor(context.Background(), "a-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less pause on closed server = %v, want ErrForbidden", err)
	}
	if err := manager.PauseChannelFor(requestTestUser("owner-a"), "a-1"); err != nil {
		t.Fatalf("owner pause: %v", err)
	}
	if err := manager.ResumeChannelFor(requestTestUser("owner-a"), "a-1"); err != nil {
		t.Fatalf("owner resume: %v", err)
	}
}
