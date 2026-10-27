package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestThrottleEventPairsEntryWithRealDrain(t *testing.T) {
	events := make(chan ThrottleEvent, 16)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalThrottled {
			return nil
		}
		if payload, ok := event.Payload.(ThrottleEvent); ok {
			select {
			case events <- payload:
			default:
			}
		}
		return nil
	})
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(context.Background(), OpenTabOptions{SessionID: session.ID, Cols: 80, Rows: 24, Detached: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab.fanout = terminal.NewFanoutWithOptions(terminal.FanoutOptions{
		QueueFrames: 1 << 20, QueueBytes: 1 << 30, Logger: manager.logger,
	})
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)

	flood := func() (chan struct{}, *sync.WaitGroup) {
		return startFakeChannelFlood(channel)
	}

	stop, wg := flood()
	entry := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	if entry.Recovered || entry.TabID != tab.ID || entry.ChannelID != "a-1" || entry.InflightBytes <= 0 || entry.Version != 1 {
		t.Fatalf("entry event = %+v", entry)
	}

	select {
	case event := <-events:
		t.Fatalf("recovered while backlog still present: %+v", event)
	case <-time.After(300 * time.Millisecond):
	}

	close(stop)
	wg.Wait()

	receiver := bindTestReceiver(t, manager, "a-1")
	var recovery ThrottleEvent
	drained := 0
	for {
		select {
		case event := <-events:
			if event.ChannelID == "a-1" && event.Recovered {
				recovery = event
			}
		default:
		}
		if recovery.Version != 0 {
			break
		}
		drained++
		if drained > 4096 {
			t.Fatal("queue drained without a recovery event")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		frame, err := receiver.Next(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := receiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.ChannelID != "a-1" || recovery.Version != 2 {
		t.Fatalf("recovery event = %+v", recovery)
	}
	if recovery.InflightBytes >= entry.InflightBytes {
		t.Fatalf("recovery bytes = %d, entry bytes = %d", recovery.InflightBytes, entry.InflightBytes)
	}

	stop, wg = flood()
	reentry := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	if reentry.Recovered || reentry.ChannelID != "a-1" || reentry.Version != 3 {
		t.Fatalf("re-entry event = %+v", reentry)
	}
	close(stop)
	wg.Wait()

	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	detachRecovery := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", true))
	if !detachRecovery.Recovered || detachRecovery.ChannelID != "a-1" || detachRecovery.Version != 4 {
		t.Fatalf("detach recovery event = %+v", detachRecovery)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-2", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	stop, wg = flood()
	reattached := waitThrottleEventMatching(t, events, throttleChannelEvent("a-2", false))
	if reattached.Recovered || reattached.TabID != tab.ID || reattached.ChannelID != "a-2" || reattached.Version != 5 {
		t.Fatalf("re-attached entry event = %+v", reattached)
	}
	close(stop)
	wg.Wait()
}

func throttleCollector() (Emitter, chan ThrottleEvent) {
	events := make(chan ThrottleEvent, 16)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalThrottled {
			return nil
		}
		if payload, ok := event.Payload.(ThrottleEvent); ok {
			select {
			case events <- payload:
			default:
			}
		}
		return nil
	})
	return emitter, events
}

// startFakeChannelFlood emits 1 KiB frames until the returned stop channel
// closes. A starved pump can make a single emit time out; the flood retries
// instead of quitting so the backlog keeps building under CPU contention, and
// it ends early only when the channel itself is closed.
func startFakeChannelFlood(channel *fakeChannel) (chan struct{}, *sync.WaitGroup) {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		frame := bytes.Repeat([]byte("x"), 1024)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := channel.emit(frame); errors.Is(err, base.ErrClosed) {
				return
			}
		}
	}()
	return stop, &wg
}

// waitThrottleEventMatching waits for a matching event. The bound only limits
// a genuine failure; with a retrying flood the event arrives as soon as the
// hub queue fills, so passing runs never wait for it.
func waitThrottleEventMatching(t *testing.T, events chan ThrottleEvent, match func(ThrottleEvent) bool) ThrottleEvent {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("missing terminal://throttled event")
			return ThrottleEvent{}
		}
	}
}

func throttleChannelEvent(channelID string, recovered bool) func(ThrottleEvent) bool {
	return func(event ThrottleEvent) bool {
		return event.ChannelID == channelID && event.Recovered == recovered
	}
}

func assertNoThrottleEvent(t *testing.T, events chan ThrottleEvent, wait time.Duration) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected terminal://throttled event: %+v", event)
	case <-time.After(wait):
	}
}

func TestThrottleRecoveryAggregatesAcrossChannels(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle-ab", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}

	fill := func(channelID string) (cancel context.CancelFunc) {
		t.Helper()
		frame := bytes.Repeat([]byte("x"), 1024)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() {
			for {
				if err := manager.bus.SendBinary(ctx, channelID, frame); err != nil {
					return
				}
			}
		}()
		return cancel
	}
	drainChannel := func(channelID string) {
		t.Helper()
		receiver := bindTestReceiver(t, manager, channelID)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			frame, err := receiver.Next(ctx)
			cancel()
			if err != nil {
				return
			}
			if err := receiver.Ack(frame.Sequence); err != nil {
				t.Fatal(err)
			}
		}
	}

	cancelA := fill("a-1")
	entryA := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	cancelA()
	if entryA.Recovered || entryA.ChannelID != "a-1" || entryA.Version != 1 {
		t.Fatalf("entry A = %+v", entryA)
	}
	cancelB := fill("b-1")
	entryB := waitThrottleEventMatching(t, events, throttleChannelEvent("b-1", false))
	cancelB()
	if entryB.Recovered || entryB.ChannelID != "b-1" || entryB.Version != 2 {
		t.Fatalf("entry B = %+v", entryB)
	}

	drainChannel("a-1")
	assertNoThrottleEvent(t, events, 300*time.Millisecond)

	drainChannel("b-1")
	recovery := waitThrottleEventMatching(t, events, throttleChannelEvent("b-1", true))
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.ChannelID != "b-1" || recovery.Version != 3 {
		t.Fatalf("recovery = %+v", recovery)
	}
}

func TestThrottleDiscardPendingPairsRecovery(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle-discard", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")

	frame := bytes.Repeat([]byte("x"), 1024)
	fillCtx, stopFill := context.WithCancel(context.Background())
	t.Cleanup(stopFill)
	sends := make(chan error, 600)
	go func() {
		for i := 0; i < 600; i++ {
			sends <- manager.bus.SendBinary(fillCtx, "a-1", frame)
		}
	}()
	entry := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	if entry.Recovered || entry.TabID != tab.ID || entry.ChannelID != "a-1" || entry.Version != 1 {
		t.Fatalf("entry = %+v", entry)
	}
	for len(sends) > 0 {
		<-sends
	}

	if err := manager.bus.DiscardPending("a-1"); err != nil {
		t.Fatal(err)
	}
	recovery := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", true))
	if !recovery.Recovered || recovery.ChannelID != "a-1" || recovery.InflightBytes != 0 || recovery.Version != 2 {
		t.Fatalf("discard recovery = %+v", recovery)
	}
	select {
	case err := <-sends:
		if err != nil {
			t.Fatalf("blocked producer not released: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked producer still blocked after discard")
	}
}

func TestThrottleFailedAttachPairsRecovery(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "throttle-attach", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)

	aReceiver := bindTestReceiver(t, manager, "a-1")
	drainerStop := make(chan struct{})
	t.Cleanup(func() { close(drainerStop) })
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			frame, err := aReceiver.Next(ctx)
			cancel()
			if err != nil {
				select {
				case <-drainerStop:
					return
				default:
				}
				if errors.Is(err, context.DeadlineExceeded) {
					continue
				}
				return
			}
			if err := aReceiver.Ack(frame.Sequence); err != nil {
				return
			}
		}
	}()

	payload := bytes.Repeat([]byte("x"), 64<<10)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 320; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := channel.emit(payload); err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for len(terminals.terminal(tab.ID).bytes()) < 20<<20 {
		if time.Now().After(deadline) {
			close(stop)
			wg.Wait()
			t.Fatal("terminal did not accumulate the replay payload")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()

	attachCtx, cancelAttach := context.WithCancel(context.Background())
	attachDone := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(attachCtx, tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 20 << 20})
		attachDone <- err
	}()
	entry := waitThrottleEventMatching(t, events, throttleChannelEvent("b-1", false))
	if entry.Recovered || entry.TabID != tab.ID || entry.ChannelID != "b-1" || entry.InflightBytes <= 0 {
		t.Fatalf("attach entry = %+v", entry)
	}
	cancelAttach()
	if err := <-attachDone; err == nil {
		t.Fatal("canceled attach succeeded")
	}
	recovery := waitThrottleEventMatching(t, events, throttleChannelEvent("b-1", true))
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.ChannelID != "b-1" || recovery.Version <= entry.Version {
		t.Fatalf("attach recovery = %+v", recovery)
	}
	assertNoThrottleEvent(t, events, 200*time.Millisecond)

	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-2", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	frame := bytes.Repeat([]byte("y"), 1024)
	fillCtx, stopFill := context.WithCancel(context.Background())
	t.Cleanup(stopFill)
	sends := make(chan error, 600)
	go func() {
		for i := 0; i < 600; i++ {
			sends <- manager.bus.SendBinary(fillCtx, "b-2", frame)
		}
	}()
	reentry := waitThrottleEventMatching(t, events, throttleChannelEvent("b-2", false))
	if reentry.Recovered || reentry.ChannelID != "b-2" || reentry.Version <= recovery.Version {
		t.Fatalf("re-entry = %+v", reentry)
	}
	stopFill()
	for len(sends) > 0 {
		<-sends
	}
	bReceiver := bindTestReceiver(t, manager, "b-2")
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		frame, err := bReceiver.Next(ctx)
		cancel()
		if err != nil {
			break
		}
		if err := bReceiver.Ack(frame.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	finalRecovery := waitThrottleEventMatching(t, events, throttleChannelEvent("b-2", true))
	if !finalRecovery.Recovered || finalRecovery.TabID != tab.ID || finalRecovery.ChannelID != "b-2" || finalRecovery.Version <= reentry.Version {
		t.Fatalf("final recovery = %+v", finalRecovery)
	}
	deadline = time.Now().Add(5 * time.Second)
	for manager.bus.Stats().QueuedFrames != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("queued frames = %+v", manager.bus.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSlowConsumerKickedAfterGraceOthersUnaffected(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter,
		SlowConsumerGrace: 60 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "kick", Kind: KindSSH})
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
	// The default 64-frame fanout aborts a starved delivery with
	// ErrFanoutQueueFull before the hub queue fills, so under CPU contention
	// the hub never reports backpressure. A large fanout makes the hub queue
	// the only bound, which is what this test exercises.
	tab.fanout = terminal.NewFanoutWithOptions(terminal.FanoutOptions{
		QueueFrames: 1 << 20, QueueBytes: 1 << 30, Logger: manager.logger,
	})
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)

	healthy := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, healthy, replayClear)
	marker := []byte("marker-after-kick")
	var sawMarker atomic.Bool
	var drained atomic.Int64
	stopDrain := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			frame, err := healthy.Next(ctx)
			cancel()
			if err != nil {
				select {
				case <-stopDrain:
					return
				default:
				}
				if errors.Is(err, context.DeadlineExceeded) {
					continue
				}
				return
			}
			drained.Add(int64(len(frame.Data)))
			if bytes.Contains(frame.Data, marker) {
				sawMarker.Store(true)
			}
			if err := healthy.Ack(frame.Sequence); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { close(stopDrain); <-drainDone })

	stopFlood, floodWG := startFakeChannelFlood(channel)

	entry := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	if entry.Recovered || entry.TabID != tab.ID || entry.ChannelID != "a-1" {
		t.Fatalf("entry = %+v", entry)
	}

	// The resync notice comes first and outranks the detach's recovery, so
	// clients show the notice instead of "recovered".
	resync := waitThrottleEventMatching(t, events, func(event ThrottleEvent) bool { return event.Resync })
	if resync.TabID != tab.ID || resync.ChannelID != "a-1" || resync.Recovered {
		t.Fatalf("kick resync = %+v", resync)
	}
	recovery := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", true))
	if !recovery.Recovered || recovery.TabID != tab.ID || recovery.ChannelID != "a-1" {
		t.Fatalf("kick recovery = %+v", recovery)
	}
	if recovery.Version >= resync.Version {
		t.Fatalf("recovery version %d not older than resync %d", recovery.Version, resync.Version)
	}
	if info := tab.Info(); info.Subscribers != 1 || info.Controller != "" {
		t.Fatalf("tab info after kick = %+v", info)
	}

	if err := channel.emit(marker); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !sawMarker.Load() {
		if time.Now().After(deadline) {
			t.Fatal("healthy subscriber stopped receiving after slow consumer kick")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(stopFlood)
	floodWG.Wait()
	if drained.Load() == 0 {
		t.Fatal("healthy subscriber starved during slow consumer kick")
	}
	if info := tab.Info(); info.Subscribers != 1 {
		t.Fatalf("kick detached more than the slow channel: %+v", info)
	}
}

func TestSlowConsumerDrainedWithinGraceIsNotKicked(t *testing.T) {
	emitter, events := throttleCollector()
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: emitter,
		SlowConsumerGrace: 500 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "grace", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	channel := connector.transport(0).channel(0)

	flood := func() (chan struct{}, *sync.WaitGroup) {
		return startFakeChannelFlood(channel)
	}
	receiver := bindTestReceiver(t, manager, "a-1")
	t.Cleanup(func() { _ = receiver.Close() })
	var paused atomic.Bool
	stopDrain := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case <-stopDrain:
				return
			default:
			}
			if paused.Load() {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			frame, err := receiver.Next(ctx)
			cancel()
			if err != nil {
				continue
			}
			if err := receiver.Ack(frame.Sequence); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { close(stopDrain); <-drainDone })
	paused.Store(true)

	stop, wg := flood()
	entry := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	if entry.Recovered || entry.ChannelID != "a-1" {
		t.Fatalf("entry = %+v", entry)
	}
	assertSettled := func() {
		manager.mu.Lock()
		_, kickPending := manager.slowKicks["a-1"]
		manager.mu.Unlock()
		if kickPending {
			t.Fatal("drained consumer retained a pending slow-consumer kick")
		}
		if info := tab.Info(); info.Subscribers != 1 {
			t.Fatalf("drained consumer was kicked: %+v", info)
		}
	}
	close(stop)
	wg.Wait()
	paused.Store(false)
	recovery := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", true))
	if !recovery.Recovered || recovery.ChannelID != "a-1" {
		t.Fatalf("recovery = %+v", recovery)
	}
	assertSettled()

	// Backpressure starts a new timer again; after draining, the channel is likewise not kicked.
	paused.Store(true)
	stop2, wg2 := flood()
	reentry := waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", false))
	if reentry.Recovered || reentry.ChannelID != "a-1" {
		t.Fatalf("reentry = %+v", reentry)
	}
	close(stop2)
	wg2.Wait()
	paused.Store(false)
	waitThrottleEventMatching(t, events, throttleChannelEvent("a-1", true))
	assertSettled()
}
