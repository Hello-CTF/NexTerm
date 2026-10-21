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

// The r13 counterexample, order 1: the old incarnation's pause lands before
// the new incarnation's ACK, then its resume lands after it. Both must be
// rejected with the floor and the high-water untouched, so the new
// incarnation's lower-but-real ACK still opens the retention floor.
func TestStreamControlOldPauseResumeRejectedBeforeNewACK(t *testing.T) {
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
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20); err != nil {
		t.Fatal(err)
	}

	// Re-attach the same channel: a new incarnation takes over.
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	newAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	if newAttachmentId == oldAttachmentId {
		t.Fatal("re-attach did not create a new incarnation")
	}
	drainAttachHeader(t, receiver)

	// The old incarnation's late pause must not touch the new subscriber's
	// floor, high-water, or paused state.
	_, err = manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("old-incarnation pause = %v, want ErrForbidden", err)
	}
	if got := tab.subscribers["a-1"].floorHighWater; got != -1 {
		t.Fatalf("floorHighWater after old pause = %d, want -1 (untouched)", got)
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != -1 {
		t.Fatalf("episodeFloor after old pause = %d, want -1 (untouched)", got)
	}
	if tab.subscribers["a-1"].paused {
		t.Fatal("old-incarnation pause paused the new subscriber")
	}

	// The new incarnation's lower-but-real ACK still opens the floor.
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, 5<<20); err != nil {
		t.Fatalf("new-incarnation ACK = %v, want nil", err)
	}
	if got := tab.recovery.floor; got != 5<<20 {
		t.Fatalf("floor after new ACK = %d, want 5 MiB", got)
	}

	// The old incarnation's late resume must not clear the new floor.
	err = manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, episodeGenFor(t, manager, info.ID, "a-1"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("old-incarnation resume = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != 5<<20 {
		t.Fatalf("floor after old resume = %d, want 5 MiB (unchanged)", got)
	}
	if got := tab.subscribers["a-1"].floorHighWater; got != 5<<20 {
		t.Fatalf("floorHighWater after old resume = %d, want 5 MiB (unchanged)", got)
	}
}

// The r13 counterexample, order 2: the new incarnation's ACK lands first, then
// the old incarnation's pause and resume arrive late. The old pause must not
// advance the floor or the high-water, and the old resume must not clear them.
func TestStreamControlOldPauseResumeRejectedAfterNewACK(t *testing.T) {
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

	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	newAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	drainAttachHeader(t, receiver)

	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, 5<<20); err != nil {
		t.Fatal(err)
	}
	// A late old pause carries a HIGHER offset than the new floor: it must not
	// advance the floor or the high-water.
	_, err = manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("old-incarnation pause = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != 5<<20 {
		t.Fatalf("floor after old pause = %d, want 5 MiB (unchanged)", got)
	}
	if got := tab.subscribers["a-1"].floorHighWater; got != 5<<20 {
		t.Fatalf("floorHighWater after old pause = %d, want 5 MiB (unchanged)", got)
	}
	// A late old resume must not clear the new floor either.
	err = manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, episodeGenFor(t, manager, info.ID, "a-1"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("old-incarnation resume = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != 5<<20 {
		t.Fatalf("floor after old resume = %d, want 5 MiB (unchanged)", got)
	}
}

// The r13 counterexample: the old incarnation's recover must not forge a
// replay on the new attachment — it is rejected and no anchor frame is sent.
func TestStreamControlOldRecoverCannotForgeReplay(t *testing.T) {
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

	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	newAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	drainAttachHeader(t, receiver)

	// The old incarnation's recover is rejected and sends nothing.
	if _, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 5<<20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old-incarnation recover = %v, want ErrForbidden", err)
	}
	expectNoFrame(t, receiver)
	if got := tab.subscribers["a-1"].episodeFloor; got != -1 {
		t.Fatalf("episodeFloor after old recover = %d, want -1 (untouched)", got)
	}
	if tab.subscribers["a-1"].paused {
		t.Fatal("old-incarnation recover paused the new subscriber")
	}
	// The new incarnation's own recover works: anchor plus the refused suffix.
	if _, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, 5<<20); err != nil {
		t.Fatalf("new-incarnation recover = %v, want nil", err)
	}
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	if got := readFramesUntil(t, receiver, (8<<20)-(5<<20)); len(got) != 3<<20 {
		t.Fatalf("replay length = %d, want 3 MiB", len(got))
	}
}

// The r13 counterexample: one client must not control another client's
// channel. A legitimate attached observer operates only its own attachment.
func TestStreamControlCrossClientForbidden(t *testing.T) {
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
	attachmentA := attachmentIdFor(t, manager, info.ID, "a-1")
	attachmentB := attachmentIdFor(t, manager, info.ID, "b-1")

	// Client A against client B's channel: rejected whether it presents B's
	// incarnation (wrong client) or its own (wrong channel binding).
	if _, err := manager.PauseChannelFor(context.Background(), info.ID, "b-1", "client-a", attachmentB, 4<<20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-client pause with B incarnation = %v, want ErrForbidden", err)
	}
	if _, err := manager.PauseChannelFor(context.Background(), info.ID, "b-1", "client-a", attachmentA, 4<<20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-client pause with A incarnation = %v, want ErrForbidden", err)
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "b-1", "client-a", attachmentB, episodeGenFor(t, manager, info.ID, "b-1")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-client resume = %v, want ErrForbidden", err)
	}
	if _, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-a", attachmentB, 4<<20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-client recover = %v, want ErrForbidden", err)
	}
	if got := tab.subscribers["b-1"].episodeFloor; got != -1 {
		t.Fatalf("channel b-1 floor = %d, want -1 (cross-client rejected)", got)
	}
	if tab.subscribers["b-1"].paused {
		t.Fatal("cross-client pause paused channel b-1")
	}

	// The legitimate observer operates its own attachment: pause with its
	// accepted offset opens its floor, resume closes it, recover replays.
	if _, err := manager.PauseChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentB, 4<<20); err != nil {
		t.Fatalf("observer own-channel pause = %v, want nil", err)
	}
	if got := tab.subscribers["b-1"].episodeFloor; got != 4<<20 {
		t.Fatalf("observer floor = %d, want 4 MiB", got)
	}
	if got := readFramesUntil(t, receiverB, 0); len(got) != 0 {
		t.Fatal("unexpected frame")
	}
	if err := channel.emit(bytes.Repeat([]byte("z"), 64<<10)); err != nil {
		t.Fatal(err)
	}
	expectNoFrame(t, receiverB)
	if got := readFramesUntil(t, receiverA, 64<<10); len(got) != 64<<10 {
		t.Fatal("channel A did not receive the chunk while B was paused")
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentB, episodeGenFor(t, manager, info.ID, "b-1")); err != nil {
		t.Fatalf("observer own-channel resume = %v, want nil", err)
	}
	if got := readFramesUntil(t, receiverB, 64<<10); len(got) != 64<<10 {
		t.Fatal("observer resume did not flush the held chunk")
	}
	if _, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "b-1", "client-b", attachmentB, 4<<20); err != nil {
		t.Fatalf("observer own-channel recover = %v, want nil", err)
	}
	anchor := receiveTestFrame(t, receiverB)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	if got := readFramesUntil(t, receiverB, 64<<10); len(got) != 64<<10 {
		t.Fatal("observer recover did not replay the suffix")
	}
}

// The r13 full-sequence counterexample: an old incarnation's 8 MiB pause and
// resume arriving after a re-attach must not suppress the new incarnation's
// 5 MiB ACK. With the floor established, sustained output to exactly
// 91,750,400 bytes stalls at the ring limit instead of evicting the refused
// suffix, and the whole sequence is redelivered byte-identical — never
// ErrRecoveryOverrun.
func TestStreamControlFullSequenceSurvivesLateOldControlFrames(t *testing.T) {
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

	const chunkSize = 64 << 10
	chunk := func(i int) []byte { return bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize) }
	var fed []byte
	fedThrough := 0
	// Phase A: the old incarnation accepts 8 MiB.
	for i := 0; i < 128; i++ {
		data := chunk(i)
		fed = append(fed, data...)
		if err := channel.emit(data); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
			t.Fatalf("phase A chunk %d mismatch", i)
		}
		fedThrough = i + 1
	}
	oldAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20); err != nil {
		t.Fatal(err)
	}

	// Phase B: re-attach; the new incarnation reports 5 MiB accepted. The old
	// incarnation's late pause and resume are rejected in both orders.
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	newAttachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	drainAttachHeader(t, receiver)
	if _, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, 8<<20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old pause = %v, want ErrForbidden", err)
	}
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, 5<<20); err != nil {
		t.Fatalf("new ACK = %v, want nil", err)
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", oldAttachmentId, episodeGenFor(t, manager, info.ID, "a-1")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old resume = %v, want ErrForbidden", err)
	}

	// Phase C: the transport keeps draining but the client accepts nothing
	// more: feed until the ring is provably at capacity with the floor
	// blocking eviction — the pump parks instead of evicting the refused
	// suffix. The pipe-held chunk is then emitted with the pump provably
	// parked, so it waits in the transport pipe and the recovery's anchor
	// is the next frame the client sees; a read-timeout stall guess could
	// instead fire while the pump is merely slow, and the late chunk would
	// overtake the anchor.
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := fedThrough; i < 4000 && !tab.recovery.needsStall(); i++ {
		data := chunk(i)
		fed = append(fed, data...)
		if err := channel.emit(data); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
			t.Fatalf("phase C chunk %d mismatch", i)
		}
		fedThrough = i + 1
	}
	if !tab.recovery.needsStall() {
		t.Fatal("producer kept reading past the ring capacity with the floor set")
	}
	if err := channel.emit(chunk(fedThrough)); err != nil {
		t.Fatal(err)
	}
	stallEmitted := true

	// The whole refused suffix [5 MiB, fed) is redelivered byte-identical.
	recoverDone := make(chan error, 1)
	go func() {
		_, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, 5<<20)
		recoverDone <- err
	}()
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	suffix := readFramesUntil(t, receiver, len(fed)-(5<<20))
	if !bytes.Equal(suffix, fed[5<<20:]) {
		t.Fatalf("refused suffix mismatch: got %d bytes want %d", len(suffix), len(fed)-(5<<20))
	}
	if err := <-recoverDone; err != nil {
		t.Fatalf("recover = %v, want the full suffix retained", err)
	}

	// Resume closes the episode; the pipe-held chunk and the rest of the
	// sequence arrive in order.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", newAttachmentId, episodeGenFor(t, manager, info.ID, "a-1")); err != nil {
		t.Fatal(err)
	}
	var tail []byte
	if stallEmitted {
		data := chunk(fedThrough)
		got := readFramesUntil(t, receiver, chunkSize)
		if !bytes.Equal(got, data) {
			t.Fatalf("pipe-held chunk %d mismatch", fedThrough)
		}
		fed = append(fed, data...)
		tail = append(tail, got...)
		fedThrough++
	}
	const totalChunks = 1400 // 1400 * 64 KiB = 91,750,400 bytes
	for i := fedThrough; i < totalChunks; i++ {
		data := chunk(i)
		fed = append(fed, data...)
		if err := channel.emit(data); err != nil {
			t.Fatalf("phase D chunk %d: %v", i, err)
		}
		got := readFramesUntil(t, receiver, chunkSize)
		if !bytes.Equal(got, data) {
			t.Fatalf("phase D chunk %d mismatch", i)
		}
		tail = append(tail, got...)
	}
	if len(fed) != 91750400 {
		t.Fatalf("fed = %d bytes, want 91,750,400", len(fed))
	}
	// The accepted sequence — 5 MiB prefix, the retained suffix, and the tail —
	// is byte-identical to the original stream.
	accepted := append(append(append([]byte{}, fed[:5<<20]...), suffix...), tail...)
	if !bytes.Equal(accepted, fed) {
		t.Fatal("accepted sequence is not byte-identical to the original stream")
	}
}

// The attach anchor carries the incarnation it was sent for, so the client
// learns the current attachmentId from the ordered stream even before the
// attach RPC response arrives.
func TestAttachAnchorCarriesAttachmentID(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: terminalFactory{}, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-anchor", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "anchor-open")

	info, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "anchor-1", ReplayBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if info.AttachmentID == 0 {
		t.Fatal("attach response AttachmentID = 0")
	}
	receiver := bindTestReceiver(t, manager, "anchor-1")
	anchor := receiveTestAnchor(t, receiver)
	if anchor.AttachmentID != info.AttachmentID {
		t.Fatalf("anchor attachmentId = %d, want the response incarnation %d", anchor.AttachmentID, info.AttachmentID)
	}
	// The anchor's incarnation is live the moment the frame is on the wire: an
	// ACK carrying it is accepted immediately, and the previous incarnation of
	// the channel is rejected.
	if err := manager.StreamAckFor(ctx, tab.ID, "anchor-1", "client-b", anchor.AttachmentID, 0); err != nil {
		t.Fatalf("ACK with the anchor incarnation = %v, want nil", err)
	}
	if err := manager.StreamAckFor(ctx, tab.ID, "anchor-1", "client-b", anchor.AttachmentID-1, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ACK with the previous incarnation = %v, want ErrForbidden", err)
	}
}

// Deferred attach: the anchor's incarnation is operational before the attach
// RPC returns. With the replay blocked on a bounded hub queue, the ACK
// carrying the anchor's attachmentId must already land — the subscriber is
// registered before the anchor frame is sent, never after the replay.
func TestStreamAckLandsWhileAttachReplayBlocked(t *testing.T) {
	bus := hub.New(hub.Options{QueueFrames: 1, QueueBytes: 1 << 20})
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: terminalFactory{}, ReconnectMax: 2,
		ReconnectBackoff: []time.Duration{0}, OpenAccess: true, Hub: bus,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "asset-deferred", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "deferred-open")
	// 4 MiB of scrollback: the replay cannot fit the 1 MiB hub queue, so the
	// attach blocks on the replay send until the test reads.
	tab.terminal.Feed(make([]byte, 4<<20))

	type attachResult struct {
		info TabInfo
		err  error
	}
	done := make(chan attachResult, 1)
	go func() {
		info, err := manager.AttachTab(ctx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "deferred-1", ReplayBytes: 4 << 20})
		done <- attachResult{info, err}
	}()
	receiver := bindTestReceiver(t, manager, "deferred-1")
	anchor := receiveTestAnchor(t, receiver)
	if anchor.AttachmentID == 0 {
		t.Fatal("anchor attachmentId = 0")
	}
	// The attach is still blocked on the replay send, but the incarnation is
	// already live: the ACK lands.
	if err := manager.StreamAckFor(ctx, tab.ID, "deferred-1", "client-b", anchor.AttachmentID, 0); err != nil {
		t.Fatalf("ACK while the attach replay is blocked = %v, want nil", err)
	}
	// Drain the replay so the attach completes; the response carries the same
	// incarnation the anchor did.
	if got := readFramesUntil(t, receiver, len(replayClear)+int(4<<20)); len(got) != len(replayClear)+int(4<<20) {
		t.Fatalf("replay bytes = %d, want %d", len(got), len(replayClear)+int(4<<20))
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.info.AttachmentID != anchor.AttachmentID {
		t.Fatalf("response attachmentId = %d, want the anchor incarnation %d", result.info.AttachmentID, anchor.AttachmentID)
	}
}

// The recovery anchor also names the incarnation it belongs to, so a client
// that somehow lost the attach response can realign; a recovery never changes
// the incarnation.
func TestRecoverAnchorCarriesCurrentAttachmentID(t *testing.T) {
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
	attachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	if _, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 1); err != nil {
		t.Fatal(err)
	}
	frame := receiveTestFrame(t, receiver)
	var anchor streamAnchor
	if err := json.Unmarshal(frame.Data, &anchor); err != nil {
		t.Fatalf("anchor frame = %q: %v", frame.Data, err)
	}
	if anchor.AttachmentID != attachmentId {
		t.Fatalf("recovery anchor attachmentId = %d, want the current incarnation %d", anchor.AttachmentID, attachmentId)
	}
	if got := readFramesUntil(t, receiver, 2); string(got) != "ne" {
		t.Fatalf("recovered frame = %q", got)
	}
}

// episodeGenFor returns the channel's current episode generation.
func episodeGenFor(t *testing.T, manager *Manager, tabID, channelID string) uint64 {
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
	return subscriber.episodeGen
}

// The r14 counterexample, order 1: a resume from a prior episode of the SAME
// attachment lands after the new episode's pause — it must be rejected and
// leave the new floor, high-water, and paused state untouched.
func TestStreamControlStaleResumeRejectedAfterNewPause(t *testing.T) {
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
	attachmentId := attachmentIdFor(t, manager, info.ID, "a-1")

	// The prior episode: pause opens it at 4 MiB, the matching resume closes
	// it.
	priorGen, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if priorGen == 0 {
		t.Fatal("pause returned generation 0")
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen); err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor after resume = %d, want -1 (closed)", got)
	}

	// The new episode: the ACK opens the floor at 8 MiB (the client advanced
	// past the prior high-water), pause joins it and returns the same
	// generation.
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 8<<20); err != nil {
		t.Fatal(err)
	}
	newGen, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if newGen <= priorGen {
		t.Fatalf("new episode generation = %d, want greater than the prior %d", newGen, priorGen)
	}

	// The prior episode's delayed resume is rejected: the new floor, the
	// high-water, and the paused state survive.
	err = manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale resume = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != 8<<20 {
		t.Fatalf("floor after stale resume = %d, want 8 MiB (unchanged)", got)
	}
	if got := tab.subscribers["a-1"].floorHighWater; got != 8<<20 {
		t.Fatalf("floorHighWater after stale resume = %d, want 8 MiB (unchanged)", got)
	}
	if !tab.subscribers["a-1"].paused {
		t.Fatal("stale resume unpaused the subscriber")
	}
	// The matching resume closes the new episode.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, newGen); err != nil {
		t.Fatalf("matching resume = %v, want nil", err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor after matching resume = %d, want -1", got)
	}
}

// The r14 counterexample, order 2: the stale resume lands BEFORE the new
// episode's ACK/pause — it must not pre-close or otherwise disturb the
// episode that opens next.
func TestStreamControlStaleResumeRejectedBeforeNewPause(t *testing.T) {
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
	attachmentId := attachmentIdFor(t, manager, info.ID, "a-1")

	priorGen, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen); err != nil {
		t.Fatal(err)
	}

	// The prior episode's resume lands again before the new episode opens:
	// with no live floor it is an explicit no-op and disturbs nothing.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen); err != nil {
		t.Fatalf("repeated prior resume with no live episode = %v, want nil (no-op)", err)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("floor after no-op resume = %d, want -1", got)
	}
	// The new episode opens at a higher offset: the no-op resume did not
	// pre-close it nor change the generation the client learns.
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 8<<20); err != nil {
		t.Fatal(err)
	}
	newGen, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if newGen <= priorGen {
		t.Fatalf("new episode generation = %d, want greater than the prior %d", newGen, priorGen)
	}
	if got := tab.recovery.floor; got != 8<<20 {
		t.Fatalf("floor = %d, want 8 MiB (the no-op resume did not pre-close it)", got)
	}
	// Now the prior generation is stale: its resume is rejected and the new
	// floor survives.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale resume = %v, want ErrForbidden", err)
	}
	if got := tab.recovery.floor; got != 8<<20 {
		t.Fatalf("floor after stale resume = %d, want 8 MiB (unchanged)", got)
	}
	// A repeated pause is idempotent: the generation the client learned stays
	// valid, so its legitimate resume cannot be mismatched.
	againGen, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if againGen != newGen {
		t.Fatalf("repeated pause generation = %d, want the idempotent %d", againGen, newGen)
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, newGen); err != nil {
		t.Fatalf("resume after repeated pause = %v, want nil", err)
	}
}

// The r14 full-sequence counterexample: on the SAME attachment (no re-attach),
// a delayed prior-episode resume must not clear the new episode's floor.
// With the floor protected, sustained output to exactly 91,750,400 bytes
// stalls at the ring limit instead of evicting the refused suffix, and the
// whole sequence is redelivered byte-identical — never ErrRecoveryOverrun.
func TestStreamControlFullSequenceSurvivesStaleResume(t *testing.T) {
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

	const chunkSize = 64 << 10
	chunk := func(i int) []byte { return bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize) }
	var fed []byte
	fedThrough := 0
	feedHealthy := func(until int) {
		for i := fedThrough; i < until; i++ {
			data := chunk(i)
			fed = append(fed, data...)
			if err := channel.emit(data); err != nil {
				t.Fatal(err)
			}
			if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
				t.Fatalf("chunk %d mismatch", i)
			}
			fedThrough = i + 1
		}
	}
	// Phase A: the client accepts 8 MiB; a prior episode opens and closes
	// normally.
	feedHealthy(128)
	attachmentId := attachmentIdFor(t, manager, info.ID, "a-1")
	priorGen, err := manager.PauseChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen); err != nil {
		t.Fatal(err)
	}

	// Phase B: the client accepts 4 MiB more and stalls at 12 MiB; the new
	// episode opens on its accepted-offset report alone (the transport keeps
	// draining; the writer refuses).
	feedHealthy(192)
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 12<<20); err != nil {
		t.Fatal(err)
	}
	// The prior episode's delayed resume arrives now: rejected, floor intact.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, priorGen); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale resume = %v, want ErrForbidden", err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.recovery.floor; got != 12<<20 {
		t.Fatalf("floor after stale resume = %d, want 12 MiB (unchanged)", got)
	}

	// Phase C: the transport keeps draining but the client accepts nothing
	// more: feed until the ring is provably at capacity with the floor
	// blocking eviction — the pump parks instead of evicting the refused
	// suffix. The pipe-held chunk is then emitted with the pump provably
	// parked, so it waits in the transport pipe and the recovery's anchor
	// is the next frame the client sees; a read-timeout stall guess could
	// instead fire while the pump is merely slow, and the late chunk would
	// overtake the anchor.
	for i := fedThrough; i < 4000 && !tab.recovery.needsStall(); i++ {
		data := chunk(i)
		fed = append(fed, data...)
		if err := channel.emit(data); err != nil {
			t.Fatal(err)
		}
		if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
			t.Fatalf("chunk %d mismatch", i)
		}
		fedThrough = i + 1
	}
	if !tab.recovery.needsStall() {
		t.Fatal("producer kept reading past the ring capacity with the floor set")
	}
	if err := channel.emit(chunk(fedThrough)); err != nil {
		t.Fatal(err)
	}
	stallEmitted := true

	// The whole refused suffix [12 MiB, fed) is redelivered byte-identical.
	recoverDone := make(chan error, 1)
	var recoverGen uint64
	go func() {
		_, gen, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 12<<20)
		recoverGen = gen
		recoverDone <- err
	}()
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	suffix := readFramesUntil(t, receiver, len(fed)-(12<<20))
	if !bytes.Equal(suffix, fed[12<<20:]) {
		t.Fatalf("refused suffix mismatch: got %d bytes want %d", len(suffix), len(fed)-(12<<20))
	}
	if err := <-recoverDone; err != nil {
		t.Fatalf("recover = %v, want the full suffix retained", err)
	}

	// The matching resume closes the episode; the pipe-held chunk and the
	// rest of the sequence arrive in order.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, recoverGen); err != nil {
		t.Fatal(err)
	}
	var tail []byte
	if stallEmitted {
		data := chunk(fedThrough)
		got := readFramesUntil(t, receiver, chunkSize)
		if !bytes.Equal(got, data) {
			t.Fatalf("pipe-held chunk %d mismatch", fedThrough)
		}
		fed = append(fed, data...)
		tail = append(tail, got...)
		fedThrough++
	}
	const totalChunks = 1400 // 1400 * 64 KiB = 91,750,400 bytes
	for i := fedThrough; i < totalChunks; i++ {
		data := chunk(i)
		fed = append(fed, data...)
		if err := channel.emit(data); err != nil {
			t.Fatalf("final chunk %d: %v", i, err)
		}
		got := readFramesUntil(t, receiver, chunkSize)
		if !bytes.Equal(got, data) {
			t.Fatalf("final chunk %d mismatch", i)
		}
		tail = append(tail, got...)
	}
	if len(fed) != 91750400 {
		t.Fatalf("fed = %d bytes, want 91,750,400", len(fed))
	}
	accepted := append(append(append([]byte{}, fed[:12<<20]...), suffix...), tail...)
	if !bytes.Equal(accepted, fed) {
		t.Fatal("accepted sequence is not byte-identical to the original stream")
	}
}

// The recover names the episode it opened (or joined) so the client can
// resume exactly that episode; a resume carrying a pre-recover generation is
// rejected.
func TestStreamControlRecoverBindsEpisodeToken(t *testing.T) {
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
	if err := channel.emit([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "one" {
		t.Fatalf("first frame = %q", got)
	}
	attachmentId := attachmentIdFor(t, manager, info.ID, "a-1")

	// The recover opens the episode and returns its generation; a resume
	// carrying the generation from before the recover is rejected and leaves
	// the episode open.
	flushed, recoverGen, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 1)
	if err != nil {
		t.Fatal(err)
	}
	if flushed != 3 {
		t.Fatalf("flushed = %d, want 3", flushed)
	}
	if recoverGen == 0 {
		t.Fatal("recover returned generation 0")
	}
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	if got := readFramesUntil(t, receiver, 2); string(got) != "ne" {
		t.Fatalf("recovered frame = %q", got)
	}
	err = manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, recoverGen-1)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("pre-recover resume = %v, want ErrForbidden", err)
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != 1 {
		t.Fatalf("episodeFloor after stale resume = %d, want 1 (unchanged)", got)
	}
	if !tab.subscribers["a-1"].paused {
		t.Fatal("stale resume unpaused the subscriber")
	}
	// The matching resume closes the recover's episode.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, recoverGen); err != nil {
		t.Fatalf("matching resume = %v, want nil", err)
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != -1 {
		t.Fatalf("episodeFloor after matching resume = %d, want -1", got)
	}
}

// The r15 full-sequence regression: sustained output to exactly
// 104,857,600 bytes across TWO recover rounds. Each round the client stalls
// accepting while the transport keeps draining, the retention floor holds
// the refused suffix, the recover redelivers it whole, and the resume fires
// only after the client caught up — the second recover must never hit
// ErrRecoveryOverrun, and the accepted sequence stays byte-identical.
func TestStreamControlFullSequenceTwoRecoverRounds(t *testing.T) {
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

	const chunkSize = 64 << 10
	chunk := func(i int) []byte { return bytes.Repeat([]byte{byte('a' + i%26)}, chunkSize) }
	var fed []byte
	fedThrough := 0
	feedHealthy := func(until int) {
		for i := fedThrough; i < until; i++ {
			data := chunk(i)
			fed = append(fed, data...)
			if err := channel.emit(data); err != nil {
				t.Fatal(err)
			}
			if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
				t.Fatalf("chunk %d mismatch", i)
			}
			fedThrough = i + 1
		}
	}
	// feedStalled keeps the transport draining while the client accepts
	// nothing more, until the ring is provably at capacity with the floor
	// blocking eviction — the pump parks instead of evicting the refused
	// suffix. The pipe-held chunk is then emitted with the pump provably
	// parked, so it waits in the transport pipe and arrives after the
	// resume; a read-timeout stall guess could instead fire while the pump
	// is merely slow, and the late chunk would overtake the anchor.
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	feedStalled := func() (stallEmitted bool) {
		for i := fedThrough; i < 4000 && !tab.recovery.needsStall(); i++ {
			data := chunk(i)
			fed = append(fed, data...)
			if err := channel.emit(data); err != nil {
				t.Fatal(err)
			}
			if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
				t.Fatalf("chunk %d mismatch", i)
			}
			fedThrough = i + 1
		}
		if !tab.recovery.needsStall() {
			t.Fatal("producer kept reading past the ring capacity with the floor set")
		}
		if err := channel.emit(chunk(fedThrough)); err != nil {
			t.Fatal(err)
		}
		return true
	}
	var attachmentId uint64
	recoverRound := func(accepted int64, episodeGen uint64) (suffix []byte) {
		recoverDone := make(chan error, 1)
		go func() {
			_, _, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, accepted)
			recoverDone <- err
		}()
		anchor := receiveTestFrame(t, receiver)
		if !bytes.Contains(anchor.Data, []byte("recovery-")) {
			t.Fatalf("anchor = %q", anchor.Data)
		}
		suffix = readFramesUntil(t, receiver, len(fed)-int(accepted))
		if !bytes.Equal(suffix, fed[accepted:]) {
			t.Fatalf("refused suffix mismatch: got %d bytes want %d", len(suffix), len(fed)-int(accepted))
		}
		if err := <-recoverDone; err != nil {
			t.Fatalf("recover(%d) = %v, want the full suffix retained", accepted, err)
		}
		if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, episodeGen); err != nil {
			t.Fatalf("resume after recover(%d) = %v", accepted, err)
		}
		return suffix
	}

	// Round 1: the client accepts 16 MiB, stalls, and recovers the suffix.
	feedHealthy(256)
	attachmentId = attachmentIdFor(t, manager, info.ID, "a-1")
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 16<<20); err != nil {
		t.Fatal(err)
	}
	stallEmitted := feedStalled()
	suffix1 := recoverRound(16<<20, episodeGenFor(t, manager, info.ID, "a-1"))
	round1End := len(fed)
	if stallEmitted {
		data := chunk(fedThrough)
		if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
			t.Fatalf("pipe-held chunk %d mismatch", fedThrough)
		}
		fed = append(fed, data...)
		fedThrough++
	}

	// Round 2: the client accepts up to 88 MiB, then stops ACKing while the
	// transport keeps draining to exactly 104,857,600 bytes; the second
	// recover must not hit ErrRecoveryOverrun — the floor held from round 1.
	feedHealthy(1408)
	middle := fed[round1End : 1408*chunkSize]
	if err := manager.StreamAckFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 88<<20); err != nil {
		t.Fatal(err)
	}
	const totalChunks = 1600 // 1600 * 64 KiB = 104,857,600 bytes
	for i := fedThrough; i < totalChunks; i++ {
		data := chunk(i)
		fed = append(fed, data...)
		if err := channel.emit(data); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if got := readFramesUntil(t, receiver, chunkSize); !bytes.Equal(got, data) {
			t.Fatalf("chunk %d mismatch", i)
		}
		fedThrough = i + 1
	}
	suffix2 := recoverRound(88<<20, episodeGenFor(t, manager, info.ID, "a-1"))
	if len(fed) != 104857600 {
		t.Fatalf("fed = %d bytes, want 104,857,600", len(fed))
	}
	accepted := append(append(append(append([]byte{}, fed[:16<<20]...), suffix1...), middle...), suffix2...)
	if !bytes.Equal(accepted, fed) {
		t.Fatal("accepted sequence is not byte-identical to the original stream")
	}
}

// The r16 server-side regression: after the recover pauses the channel for
// the replay, a stale-token resume is rejected and the channel stays
// paused; the current-token resume actually unpauses it and clears the
// retention floor — the "second resume succeeds and lifts the pause"
// contract the client's retry depends on.
func TestStreamControlResumeRetryActuallyUnpauses(t *testing.T) {
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
	if err := channel.emit([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := readFramesUntil(t, receiver, 3); string(got) != "one" {
		t.Fatalf("first frame = %q", got)
	}
	attachmentId := attachmentIdFor(t, manager, info.ID, "a-1")

	// The recover pauses the channel for the replay and returns its episode.
	flushed, recoverGen, err := manager.RecoverChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, 1)
	if err != nil {
		t.Fatal(err)
	}
	if flushed != 3 {
		t.Fatalf("flushed = %d, want 3", flushed)
	}
	anchor := receiveTestFrame(t, receiver)
	if !bytes.Contains(anchor.Data, []byte("recovery-")) {
		t.Fatalf("anchor = %q", anchor.Data)
	}
	if got := readFramesUntil(t, receiver, 2); string(got) != "ne" {
		t.Fatalf("recovered frame = %q", got)
	}
	if !tab.subscribers["a-1"].paused {
		t.Fatal("recover did not pause the channel for the replay")
	}

	// A stale-token resume is rejected: the channel stays paused and the
	// floor survives.
	err = manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, recoverGen-1)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale resume = %v, want ErrForbidden", err)
	}
	if !tab.subscribers["a-1"].paused {
		t.Fatal("stale resume unpaused the channel")
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != 1 {
		t.Fatalf("episodeFloor after stale resume = %d, want 1 (unchanged)", got)
	}

	// The current-token resume actually unpauses the channel and clears the
	// floor — the retry the client schedules after a transient failure.
	if err := manager.ResumeChannelFor(context.Background(), info.ID, "a-1", "client-a", attachmentId, recoverGen); err != nil {
		t.Fatalf("current-token resume = %v, want nil", err)
	}
	if tab.subscribers["a-1"].paused {
		t.Fatal("current-token resume did not unpause the channel")
	}
	if got := tab.subscribers["a-1"].episodeFloor; got != -1 {
		t.Fatalf("episodeFloor after current-token resume = %d, want -1", got)
	}
	if got := tab.recovery.floor; got != -1 {
		t.Fatalf("ring floor after current-token resume = %d, want -1", got)
	}
}
