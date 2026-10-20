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

// tryReadFrames reads want bytes with a short deadline and reports whether
// they arrived intact; a timeout means the producer stalled.
func tryReadFrames(t *testing.T, receiver *hub.Receiver, want int, chunk []byte) bool {
	t.Helper()
	var got []byte
	deadline := time.Now().Add(200 * time.Millisecond)
	for len(got) < want {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		frame, err := receiver.Next(ctx)
		cancel()
		if err != nil {
			return false
		}
		got = append(got, frame.Data...)
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	return bytes.Equal(got, chunk)
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
	if _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 3); err != nil {
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
	if _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), acceptedA); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiverA)
	// b-1 refused 1 MiB: anchor frame followed by the refused suffix.
	if _, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB); err != nil {
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
	if err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), -1); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("three")); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiver)

	if _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 3); err != nil {
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

	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1")); err != nil {
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

	// Sustained output past the ring's 64 MiB capacity with a healthy
	// consumer: a client that then claims an accepted offset older than the
	// retention window must get an explicit overrun, never a short replay
	// that silently drops the evicted prefix.
	const chunkSize = 64 << 10
	const chunks = 1120
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

	if _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 4<<20); !errors.Is(err, ErrRecoveryOverrun) {
		t.Fatalf("recover = %v, want ErrRecoveryOverrun", err)
	}
	expectNoFrame(t, receiver)

	// The boundary is exact: a refused suffix that still fits the window is
	// redelivered whole and byte-identical.
	const accepted = 69 << 20
	if _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), accepted); err != nil {
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
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")

	if err := channel.emit([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "one" {
		t.Fatalf("first frame = %q", got)
	}

	if err := manager.PauseChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentIdFor(t, manager, tab.ID, "a-1"), -1); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("two")); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiver)

	if err := manager.ResumeChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentIdFor(t, manager, tab.ID, "a-1")); err != nil {
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

	if err := manager.PauseChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentIdFor(t, manager, tab.ID, "a-1"), -1); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverB, 1); string(got) != "x" {
		t.Fatalf("other subscriber frame = %q", got)
	}
	expectNoFrame(t, receiverA)

	if err := manager.ResumeChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentIdFor(t, manager, tab.ID, "a-1")); err != nil {
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

	if err := manager.PauseChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentIdFor(t, manager, tab.ID, "a-1"), -1); err != nil {
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

	if err := manager.ResumeChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentIdFor(t, manager, tab.ID, "a-1")); err != nil {
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
	if err := manager.PauseChannelFor(context.Background(), info.ID, "a-local", "client-a", attachmentIdFor(t, manager, info.ID, "a-local"), -1); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Tab(info.ID); err != nil {
		t.Fatalf("ephemeral tab reaped by pause: %v", err)
	}
	if got := connector.transport(0).channel(0).closeCount.Load(); got != 0 {
		t.Fatalf("PTY closed by pause: close count = %d", got)
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-local", "client-a", attachmentIdFor(t, manager, info.ID, "a-local")); err != nil {
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
	tab := openTestTab(t, manager, session, "client-a", "a-1")

	attachmentId := attachmentIdFor(t, manager, tab.ID, "a-1")
	if err := manager.PauseChannelFor(requestTestUser("owner-b"), tab.ID, "a-1", "client-a", attachmentId, -1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other owner pause = %v, want ErrForbidden", err)
	}
	if err := manager.PauseChannelFor(context.Background(), tab.ID, "a-1", "client-a", attachmentId, -1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("identity-less pause on closed server = %v, want ErrForbidden", err)
	}
	if err := manager.PauseChannelFor(requestTestUser("owner-a"), tab.ID, "a-1", "client-a", attachmentId, -1); err != nil {
		t.Fatalf("owner pause: %v", err)
	}
	if err := manager.ResumeChannelFor(requestTestUser("owner-a"), tab.ID, "a-1", "client-a", attachmentId); err != nil {
		t.Fatalf("owner resume: %v", err)
	}
}

// The r6 counterexample: a real saturated client keeps draining the
// transport while its writer refuses output, so its accepted offset stalls
// while the producer keeps advancing. With a healthy peer on the same tab,
// recovery must still redeliver the whole refused suffix — never
// ErrRecoveryOverrun, never a short replay.
func TestRecoverChannelForSaturatedMultiChannelClientKeepsSuffix(t *testing.T) {
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

	// Sustained output well past the old 32 MiB ring: channel B accepted only
	// the first 16 MiB (its writer cap); channel A stays healthy.
	const chunkSize = 64 << 10
	const chunks = 900
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

	// B's accepted offset stalled at 16 MiB: the 40.25 MiB suffix must be
	// redelivered whole and byte-identical. The replay streams while the
	// client reads, exactly as the production transport does — the recover
	// RPC only returns once the whole suffix is enqueued.
	const acceptedB = 16 << 20
	recoverErr := make(chan error, 1)
	go func() {
		_, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB)
		recoverErr <- err
	}()
	anchorB := receiveTestFrame(t, receiverB)
	if !bytes.Contains(anchorB.Data, []byte("recovery-")) {
		t.Fatalf("anchor B = %q", anchorB.Data)
	}
	suffixB := readFramesUntil(t, receiverB, len(fed)-acceptedB)
	if !bytes.Equal(suffixB, fed[acceptedB:]) {
		t.Fatalf("channel B suffix mismatch: got %d bytes want %d", len(suffixB), len(fed)-acceptedB)
	}
	if err := <-recoverErr; err != nil {
		t.Fatalf("recover = %v, want the full suffix retained", err)
	}
}

// Recovery pauses the channel so live output never interleaves with the
// replay: the coalescer holds the post-recovery bytes, the producer stalls
// instead of overrunning the ring, and the resume flush delivers them in
// order behind the replay — the full sequence stays contiguous.
func TestRecoverChannelForPauseHoldsLiveOutputForOrderedResume(t *testing.T) {
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

	const chunkSize = 64 << 10
	var fed []byte
	feedA := func(i int) bool {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		fed = append(fed, chunk...)
		if err := channel.emit(chunk); err != nil {
			return false
		}
		if got := readFramesUntil(t, receiverA, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel A chunk mismatch at %d", i)
		}
		return true
	}
	feedBoth := func(i int) bool {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		if !feedA(i) {
			return false
		}
		if got := readFramesUntil(t, receiverB, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel B chunk mismatch at %d", i)
		}
		return true
	}
	for i := 0; i < 512; i++ {
		if !feedBoth(i) {
			t.Fatal("producer stalled before the recovery")
		}
	}
	const acceptedB = 16 << 20
	recoverDone := make(chan error, 1)
	go func() {
		_, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB)
		recoverDone <- err
	}()
	anchorB := receiveTestFrame(t, receiverB)
	if !bytes.Contains(anchorB.Data, []byte("recovery-")) {
		t.Fatalf("anchor B = %q", anchorB.Data)
	}
	if got := readFramesUntil(t, receiverB, len(fed)-acceptedB); !bytes.Equal(got, fed[acceptedB:]) {
		t.Fatal("replay mismatch")
	}
	if err := <-recoverDone; err != nil {
		t.Fatal(err)
	}
	// The channel is paused for the replay now: the next chunk fills B's
	// coalescer, and the one after stalls the producer — it must not reach
	// channel A either.
	if !feedA(512) {
		t.Fatal("producer stalled before the coalescer filled")
	}
	chunk513 := bytes.Repeat([]byte{byte('a' + 513%26)}, chunkSize)
	if err := channel.emit(chunk513); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiverA)

	// Resume flushes the held bytes in order behind the replay: B reads the
	// post-recovery chunks contiguously, the producer continues, and the
	// stalled chunk reaches A as well.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverB, len(fed)-(32<<20)); !bytes.Equal(got, fed[32<<20:]) {
		t.Fatal("post-recovery stream B mismatch")
	}
	if got := readFramesUntil(t, receiverA, chunkSize); !bytes.Equal(got, chunk513) {
		t.Fatal("stalled chunk did not reach channel A after the resume")
	}
	if !feedA(len(fed) / chunkSize) {
		t.Fatal("producer did not resume after the resume flush")
	}
}

// The recovery anchor must not overtake frames already queued for the
// subscriber: with a one-frame hub queue, b and c are still in the fanout
// queue when recovery runs, and the client must read b, c, then the anchor,
// then the replay — never anchor, stale frames, then a duplicate replay.
func TestRecoverChannelForOrdersAnchorBehindPendingFrames(t *testing.T) {
	bus := hub.New(hub.Options{QueueFrames: 1, QueueBytes: 1 << 20})
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), ReconnectMax: 2,
		ReconnectBackoff: []time.Duration{0}, OpenAccess: true, Hub: bus,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset-local", Name: "local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
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

	if err := channel.emit([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 1); string(got) != "a" {
		t.Fatalf("first frame = %q", got)
	}
	if err := channel.emit([]byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := channel.emit([]byte("c")); err != nil {
		t.Fatal(err)
	}
	// Wait for the coalescer to flush "bc" into the hub queue: it must be
	// flushed (counted in the channel's flushed offset) but still pending
	// (unread) when recovery runs.
	deadline := time.Now().Add(2 * time.Second)
	for manager.bus.Stats().QueuedFrames == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if manager.bus.Stats().QueuedFrames == 0 {
		t.Fatal("coalescer did not flush the pending frames")
	}

	recoverDone := make(chan error, 1)
	go func() {
		_, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentIdFor(t, manager, info.ID, "a-1"), 1)
		recoverDone <- err
	}()
	// b and c were already queued for the subscriber: they deliver first
	// (batched by the coalescer), then the anchor, then the replay of the
	// refused suffix — each byte exactly once.
	if got := readFramesUntil(t, receiver, 2); string(got) != "bc" {
		t.Fatalf("pending frames = %q, want bc", got)
	}
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("frame after bc = %q, want the recovery anchor", anchor.Data)
	}
	if got := readFramesUntil(t, receiver, 2); string(got) != "bc" {
		t.Fatalf("replay = %q, want bc", got)
	}
	if err := <-recoverDone; err != nil {
		t.Fatal(err)
	}
}

// The retention floor must stop eviction even before a recover arrives: a
// saturated client that pauses with its accepted offset stalls the producer
// at the ring limit instead of letting sustained output evict the refused
// suffix. The whole suffix is then redelivered byte-identical.
func TestRecoverChannelForFloorRetainsSuffixBeforeRecover(t *testing.T) {
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

	const chunkSize = 64 << 10
	var fed []byte
	feedA := func(i int) bool {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		fed = append(fed, chunk...)
		if err := channel.emit(chunk); err != nil {
			return false
		}
		if got := readFramesUntil(t, receiverA, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel A chunk mismatch at %d", i)
		}
		return true
	}
	feedBoth := func(i int) bool {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		if !feedA(i) {
			return false
		}
		if got := readFramesUntil(t, receiverB, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel B chunk mismatch at %d", i)
		}
		return true
	}
	// B accepts 16 MiB while the producer advances past it, then pauses with
	// its accepted offset: the retention floor opens at 16 MiB.
	for i := 0; i < 512; i++ {
		if !feedBoth(i) {
			t.Fatal("producer stalled before the floor opened")
		}
	}
	const acceptedB = 16 << 20
	if err := manager.PauseChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB); err != nil {
		t.Fatal(err)
	}

	// Sustained output past the ring's capacity: the producer stalls at the
	// retention floor instead of evicting the refused suffix. The next chunk
	// fills B's coalescer; the one after stalls the producer and must not
	// reach channel A either.
	if !feedA(512) {
		t.Fatal("producer stalled before the coalescer filled")
	}
	chunk512 := bytes.Repeat([]byte{byte('a' + 512%26)}, chunkSize)
	chunk513 := bytes.Repeat([]byte{byte('a' + 513%26)}, chunkSize)
	if err := channel.emit(chunk513); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiverA)

	// The whole refused suffix is redelivered byte-identical — no overrun.
	// Chunk 512 went to the paused coalescer, so the replay covers exactly
	// [16 MiB, 32 MiB).
	recoverDone := make(chan error, 1)
	go func() {
		_, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB)
		recoverDone <- err
	}()
	anchorB := receiveTestFrame(t, receiverB)
	if !bytes.Contains(anchorB.Data, []byte("recovery-")) {
		t.Fatalf("anchor B = %q", anchorB.Data)
	}
	if got := readFramesUntil(t, receiverB, (32<<20)-acceptedB); !bytes.Equal(got, fed[acceptedB:32<<20]) {
		t.Fatal("recovered suffix mismatch")
	}
	if err := <-recoverDone; err != nil {
		t.Fatal(err)
	}
	// Resume flushes the held chunk in order behind the replay.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiverB, chunkSize); !bytes.Equal(got, chunk512) {
		t.Fatal("post-recovery stream B mismatch")
	}
	if got := readFramesUntil(t, receiverA, chunkSize); !bytes.Equal(got, chunk513) {
		t.Fatal("stalled chunk did not reach channel A after the resume")
	}
}

// The retention floor must be established by a timely accepted-offset signal,
// not by a delayed RPC: with pause and recover both delayed until after the
// ring would have wrapped, the producer must already have stalled at the
// floor so the full refused suffix survives.
func TestRecoverChannelForFloorStallsBeforeDelayedRPC(t *testing.T) {
	manager, connector, _, connected := openOpenTestSession(t, KindLocal)
	info, err := manager.CreateTab(context.Background(), CreateTabOptions{SessionID: connected.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	receiverA := bindTestReceiver(t, manager, "a-1")
	receiverB := bindTestReceiver(t, manager, "b-1")
	drainAttachHeader(t, receiverA)
	drainAttachHeader(t, receiverB)

	const chunkSize = 64 << 10
	var fed []byte
	feedA := func(i int) bool {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		fed = append(fed, chunk...)
		if err := channel.emit(chunk); err != nil {
			return false
		}
		if got := readFramesUntil(t, receiverA, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel A chunk mismatch at %d", i)
		}
		return true
	}
	feedBoth := func(i int) bool {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		if !feedA(i) {
			return false
		}
		if got := readFramesUntil(t, receiverB, chunkSize); !bytes.Equal(got, chunk) {
			t.Fatalf("channel B chunk mismatch at %d", i)
		}
		return true
	}
	for i := 0; i < 256; i++ {
		if !feedBoth(i) {
			t.Fatal("producer stalled before the floor opened")
		}
	}
	const acceptedB = 16 << 20
	// B reports its accepted offset through the typed control path: the floor
	// opens at 16 MiB without waiting for the pause/recover RPCs.
	if err := manager.StreamAckFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB); err != nil {
		t.Fatal(err)
	}

	// Sustained output past the ring's capacity: the producer stalls at the
	// retention floor instead of evicting the refused suffix. Both transports
	// keep draining, exactly like the real saturated client and its healthy
	// peer. Feed until a chunk no longer arrives — that is the floor stall.
	stalled := false
	for i := 256; i < 4000 && !stalled; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize)
		if err := channel.emit(chunk); err != nil {
			stalled = true
			break
		}
		if !tryReadFrames(t, receiverA, chunkSize, chunk) {
			stalled = true
		}
		if !tryReadFrames(t, receiverB, chunkSize, chunk) {
			stalled = true
		}
		if !stalled {
			fed = append(fed, chunk...)
		}
	}
	if !stalled {
		t.Fatal("producer kept reading past the ring capacity with the floor set")
	}

	// The whole refused suffix is redelivered byte-identical — no overrun,
	// even though the pause/recover RPCs only now arrive.
	recoverDone := make(chan error, 1)
	go func() {
		_, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentIdFor(t, manager, info.ID, "b-1"), acceptedB)
		recoverDone <- err
	}()
	anchorB := receiveTestFrame(t, receiverB)
	if !bytes.Contains(anchorB.Data, []byte("recovery-")) {
		t.Fatalf("anchor B = %q", anchorB.Data)
	}
	if got := readFramesUntil(t, receiverB, len(fed)-acceptedB); !bytes.Equal(got, fed[acceptedB:]) {
		t.Fatal("recovered suffix mismatch")
	}
	if err := <-recoverDone; err != nil {
		t.Fatal(err)
	}
}
