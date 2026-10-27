package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

func newSlowSubscriberFixture(t *testing.T, queueFrames, queueBytes int, kinds ...string) (*Manager, *Tab) {
	t.Helper()
	kind := KindSSH
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	bus := hub.New(hub.Options{QueueFrames: 2, QueueBytes: 64 << 20})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: terminalFactory{}, Hub: bus, Logger: logger,
	})
	t.Cleanup(func() {
		_ = manager.Close()
		_ = bus.Close()
	})
	connected, err := manager.Connect(context.Background(), Asset{ID: "slow-subscriber", Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true, Ephemeral: kind == KindLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	tab.fanout.DetachAll()
	tab.fanout = terminal.NewFanoutWithOptions(terminal.FanoutOptions{
		QueueFrames: queueFrames, QueueBytes: queueBytes, Logger: logger,
	})
	tab.mu.Unlock()
	return manager, tab
}

func attachSlowSubscriberReceiver(t *testing.T, manager *Manager, tab *Tab, channelID string) (*hub.Receiver, streamAnchor) {
	t.Helper()
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: channelID + "-client", ChannelID: channelID, ReplayBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, channelID)
	anchor := receiveTestAnchor(t, receiver)
	assertFrameData(t, receiver, replayClear)
	return receiver, anchor
}

func feedSlowSubscriberFrame(t *testing.T, manager *Manager, tab *Tab, data []byte) {
	t.Helper()
	if err := feedSlowSubscriberFrameResult(manager, tab, data); err != nil {
		t.Fatal(err)
	}
}

func feedSlowSubscriberFrameResult(manager *Manager, tab *Tab, data []byte) error {
	tab.mu.Lock()
	generation := tab.generation
	tab.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- manager.feed(context.Background(), tab, generation, data) }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return errors.New("feed blocked behind a slow subscriber")
	}
}

// assertObserverData reads the next expected bytes from an observer's
// stream. Once the slow controller is gone nobody answers queries, so the
// observer also receives "answered" stream-class frames ahead of the bytes;
// they are checked and skipped, and the bytes must arrive contiguously.
func assertObserverData(t *testing.T, receiver *hub.Receiver, expected []byte) {
	t.Helper()
	var got []byte
	for len(got) < len(expected) {
		frame := receiveTestFrame(t, receiver)
		if frame.Kind == hub.FrameJSON {
			var class streamClassFrame
			if err := json.Unmarshal(frame.Data, &class); err != nil || class.Type != streamClassType || class.Kind != classAnswered {
				t.Fatalf("observer JSON frame = %s, want an answered stream-class frame", frame.Data)
			}
			continue
		}
		got = append(got, frame.Data...)
	}
	if !bytes.Equal(got, expected) {
		t.Fatalf("observer bytes = %q, want %q", got, expected)
	}
}

func TestSlowSubscriberDisconnectReplaysFromLastContinuousSequence(t *testing.T) {
	manager, tab := newSlowSubscriberFixture(t, 2, 4<<20)
	slow, initialAnchor := attachSlowSubscriberReceiver(t, manager, tab, "slow")
	fast, _ := attachSlowSubscriberReceiver(t, manager, tab, "fast")

	prefix := bytes.Repeat([]byte("p"), outputCoalesceMax)
	feedSlowSubscriberFrame(t, manager, tab, prefix)
	assertFrameData(t, slow, prefix)
	assertObserverData(t, fast, prefix)
	source := append([]byte(nil), prefix...)
	oldStream := append([]byte(nil), prefix...)

	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var cleanupOnce sync.Once
	var releaseOnce sync.Once
	previousHook := manager.hooks.channelCloseAfterCapture
	manager.hooks.channelCloseAfterCapture = func(channelID string) {
		if channelID != "slow" {
			return
		}
		cleanupOnce.Do(func() {
			close(cleanupStarted)
			<-releaseCleanup
		})
	}
	t.Cleanup(func() {
		manager.hooks.channelCloseAfterCapture = previousHook
		releaseOnce.Do(func() { close(releaseCleanup) })
	})

	disconnected := false
	for index := range 8 {
		chunk := bytes.Repeat([]byte{byte('a' + index)}, outputCoalesceMax)
		source = append(source, chunk...)
		feedSlowSubscriberFrame(t, manager, tab, chunk)
		assertObserverData(t, fast, chunk)
		select {
		case <-cleanupStarted:
			disconnected = true
		default:
		}
		if disconnected {
			break
		}
	}
	if !disconnected {
		select {
		case <-cleanupStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("slow subscriber did not enter overflow cleanup")
		}
	}

	afterAbort := bytes.Repeat([]byte("m"), outputCoalesceMax)
	source = append(source, afterAbort...)
	feedSlowSubscriberFrame(t, manager, tab, afterAbort)
	assertObserverData(t, fast, afterAbort)
	residual := []byte("residual-after-abort")
	source = append(source, residual...)
	feedSlowSubscriberFrame(t, manager, tab, residual)
	assertObserverData(t, fast, residual)
	releaseOnce.Do(func() { close(releaseCleanup) })

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		frame, err := slow.Next(ctx)
		cancel()
		if errors.Is(err, hub.ErrClosed) {
			break
		}
		if err != nil {
			t.Fatalf("slow receiver error = %v, want explicit channel close after queued frames", err)
		}
		if frame.Kind != hub.FrameBinary {
			t.Fatalf("old stream frame kind = %d, want binary", frame.Kind)
		}
		oldStream = append(oldStream, frame.Data...)
		if err := slow.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	_ = slow.Close()
	if bytes.Contains(oldStream, residual) {
		t.Fatal("post-abort residual crossed the discarded gap and advanced the old stream")
	}
	if len(oldStream) > len(source) {
		t.Fatalf("old stream has %d bytes, exceeds original %d", len(oldStream), len(source))
	}
	consumed := initialAnchor.StreamSeq + uint64(len(oldStream))
	wantReplay := source[len(oldStream):]

	resumed := bindTestReceiver(t, manager, "slow")
	attachDone := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
			ClientID: "slow-client", ChannelID: "slow", ReplayBytes: 0,
			FromSeq: consumed, FromEpoch: tab.streamEpoch,
		})
		attachDone <- err
	}()
	resumedAnchor := receiveTestAnchor(t, resumed)
	if resumedAnchor.Expired || resumedAnchor.StreamSeq != consumed || resumedAnchor.ControlBytes != 0 {
		t.Fatalf("resumed anchor = %+v, want continuous sequence %d", resumedAnchor, consumed)
	}
	var gotReplay []byte
	for len(gotReplay) < len(wantReplay) {
		frame := receiveTestFrame(t, resumed)
		if frame.Kind != hub.FrameBinary {
			t.Fatalf("replay frame kind = %d, want binary", frame.Kind)
		}
		gotReplay = append(gotReplay, frame.Data...)
	}
	if !bytes.Equal(gotReplay, wantReplay) {
		t.Fatalf("replay differs from the continuous output gap: got %d bytes want %d", len(gotReplay), len(wantReplay))
	}
	select {
	case err := <-attachDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reattach did not finish after replay drained")
	}

	tail := bytes.Repeat([]byte("z"), outputCoalesceMax)
	feedSlowSubscriberFrame(t, manager, tab, tail)
	assertFrameData(t, resumed, tail)
	assertObserverData(t, fast, tail)
	recovered := append(bytes.Clone(oldStream), gotReplay...)
	recovered = append(recovered, tail...)
	wantComplete := append(bytes.Clone(source), tail...)
	if !bytes.Equal(recovered, wantComplete) {
		t.Fatalf("old stream + replay + live differs from source: got %d bytes want %d", len(recovered), len(wantComplete))
	}
}

func TestSessionPublisherP99WithBlockedSubscriber(t *testing.T) {
	const samples = 256
	manager, tab := newSlowSubscriberFixture(t, samples+1, 32<<20)
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "slow-client", ChannelID: "slow", ReplayBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	fast, _ := attachSlowSubscriberReceiver(t, manager, tab, "fast")
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	var slowOnce sync.Once
	tab.fanout.Detach("slow")
	tab.fanout.AttachWithOptions("slow", terminal.SinkFunc(func(ctx context.Context, _ []byte) error {
		slowOnce.Do(func() { close(slowStarted) })
		select {
		case <-releaseSlow:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), terminal.FanoutSubscriberOptions{})
	t.Cleanup(func() { close(releaseSlow) })
	warm := []byte("warm")
	if err := manager.feed(context.Background(), tab, tab.generation, warm); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, fast, warm)
	select {
	case <-slowStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("slow subscriber did not enter the send barrier")
	}
	payload := bytes.Repeat([]byte("p"), 16)
	durations := make([]time.Duration, 0, samples)
	for range samples {
		started := time.Now()
		if err := manager.feed(context.Background(), tab, tab.generation, payload); err != nil {
			t.Fatal(err)
		}
		assertFrameData(t, fast, payload)
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(len(durations)*99)/100]
	if p99 >= 100*time.Millisecond {
		t.Fatalf("session feed-to-fast-receiver P99 with a blocked subscriber = %s", p99)
	}
	t.Logf("session feed-to-fast-receiver P99 with one unbound slow channel and one drained fast channel (%d x %d bytes): %s", samples, len(payload), p99)
	manager.flushSubscriberOutput(tab.ID, "slow")
	stats, ok := tab.fanout.SubscriberStats("slow")
	wantLagBytes := len(warm) + samples*len(payload)
	if !ok || stats.LagFrames == 0 || stats.LagBytes != wantLagBytes || stats.Gaps != 0 || stats.DroppedFrames != 0 {
		t.Fatalf("slow subscriber stats = %+v, present %v", stats, ok)
	}
	if err := manager.DetachChannel("slow"); err != nil {
		t.Fatal(err)
	}
}

func TestSlowSubscriberOverflowReapsEphemeralTab(t *testing.T) {
	manager, tab := newSlowSubscriberFixture(t, 2, 4<<20, KindLocal)
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "slow-client", ChannelID: "slow", ReplayBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	for index := range 8 {
		err := feedSlowSubscriberFrameResult(manager, tab, bytes.Repeat([]byte{byte('a' + index)}, outputCoalesceMax))
		if errors.Is(err, ErrStaleGeneration) || errors.Is(err, ErrTabClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if tab.Info().Subscribers == 0 {
			break
		}
	}
	waitFor(t, func() bool {
		_, err := manager.Tab(tab.ID)
		return errors.Is(err, ErrTabNotFound)
	})
}

func TestSubscriberDisconnectIgnoresReplacedAttachment(t *testing.T) {
	manager, tab := newSlowSubscriberFixture(t, 4, 4<<20)
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "old", ChannelID: "shared", ReplayBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	oldIncarnation := manager.channelIncarnations["shared"]
	manager.mu.Unlock()
	tab.mu.Lock()
	oldAttachment := tab.subscribers["shared"].co
	tab.mu.Unlock()
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{
		ClientID: "new", ChannelID: "shared", ReplayBytes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	manager.disconnectSubscriberOutput(tab, "shared", oldIncarnation, oldAttachment, terminal.FanoutSubscriberStats{}, terminal.ErrFanoutQueueFull)
	manager.mu.Lock()
	currentIncarnation := manager.channelIncarnations["shared"]
	manager.mu.Unlock()
	tab.mu.Lock()
	currentAttachment := tab.subscribers["shared"].co
	tab.mu.Unlock()
	if currentIncarnation != oldIncarnation || currentAttachment == oldAttachment || tab.Info().Subscribers != 1 {
		t.Fatalf("stale disconnect affected replacement: incarnation %d/%d subscribers %d", oldIncarnation, currentIncarnation, tab.Info().Subscribers)
	}
}

// A subscriber dropped for a full fanout queue gets a resync notice before
// its channel closes; a healthy subscriber gets none.
func TestFanoutOverflowEmitsResyncNotice(t *testing.T) {
	events := make(chan ThrottleEvent, 64)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if payload, ok := event.Payload.(ThrottleEvent); ok && event.Topic == TopicTerminalThrottled && payload.Resync {
			events <- payload
		}
		return nil
	})
	bus := hub.New(hub.Options{QueueFrames: 2, QueueBytes: 64 << 20})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: terminalFactory{}, Hub: bus, Logger: logger, Emitter: emitter})
	t.Cleanup(func() {
		_ = manager.Close()
		_ = bus.Close()
	})
	connected, err := manager.Connect(context.Background(), Asset{ID: "overflow-resync", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	tab.fanout.DetachAll()
	tab.fanout = terminal.NewFanoutWithOptions(terminal.FanoutOptions{QueueFrames: 2, QueueBytes: 4 << 20, Logger: logger})
	tab.mu.Unlock()
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "slow-client", ChannelID: "slow"}); err != nil {
		t.Fatal(err)
	}
	fast, _ := attachSlowSubscriberReceiver(t, manager, tab, "fast")
	for index := 0; tab.Info().Subscribers > 1; index++ {
		if index == 64 {
			t.Fatal("slow subscriber was never dropped")
		}
		chunk := bytes.Repeat([]byte{byte('a' + index%26)}, outputCoalesceMax)
		feedSlowSubscriberFrame(t, manager, tab, chunk)
		for received := 0; received < len(chunk); {
			frame := receiveTestFrame(t, fast)
			if frame.Kind != hub.FrameJSON {
				received += len(frame.Data)
			}
		}
	}
	select {
	case event := <-events:
		if event.TabID != tab.ID || event.ChannelID != "slow" || event.Recovered {
			t.Fatalf("resync notice = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no resync notice for the dropped subscriber")
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected second resync notice %+v", event)
	default:
	}
}
