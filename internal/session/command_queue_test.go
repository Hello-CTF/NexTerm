package session

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

type blockingCommandSink struct {
	mu          sync.Mutex
	records     []CommandRecord
	started     chan struct{}
	startOnce   sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func newBlockingCommandSink() *blockingCommandSink {
	return &blockingCommandSink{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingCommandSink) RecordCommand(_ context.Context, record CommandRecord) {
	s.startOnce.Do(func() { close(s.started) })
	<-s.release
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *blockingCommandSink) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func (s *blockingCommandSink) snapshot() []CommandRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CommandRecord(nil), s.records...)
}

func discardCommandLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAsyncCommandSinkFullQueueBlocksSourceWithoutLoss(t *testing.T) {
	sink := newBlockingCommandSink()
	queue := newAsyncCommandSink(sink, discardCommandLogger(), 2, nil)
	t.Cleanup(func() {
		sink.unblock()
		queue.Close()
	})
	record := func(command string) CommandRecord {
		return CommandRecord{SessionID: "session-1", TabID: "tab-1", Command: command}
	}
	queue.RecordCommand(context.Background(), record("one"))
	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("command worker did not enter the slow sink")
	}
	queue.RecordCommand(context.Background(), record("two"))
	queue.RecordCommand(context.Background(), record("three"))
	attempted := make(chan struct{})
	enqueued := make(chan struct{})
	go func() {
		close(attempted)
		queue.RecordCommand(context.Background(), record("four"))
		close(enqueued)
	}()
	<-attempted
	select {
	case <-enqueued:
		t.Fatal("a command record was dropped or bypassed the full queue")
	case <-time.After(100 * time.Millisecond):
	}
	sink.unblock()
	select {
	case <-enqueued:
	case <-time.After(5 * time.Second):
		t.Fatal("command source did not resume after the queue drained")
	}
	queue.Close()
	records := sink.snapshot()
	if len(records) != 4 {
		t.Fatalf("records = %+v, want all four", records)
	}
	for index, command := range []string{"one", "two", "three", "four"} {
		if records[index].Command != command {
			t.Fatalf("record %d = %q, want %q", index, records[index].Command, command)
		}
	}
	if queue.rejected.Load() != 0 || queue.failures.Load() != 0 {
		t.Fatalf("unexpected command loss: rejected=%d failures=%d", queue.rejected.Load(), queue.failures.Load())
	}
}

func TestManagerCloseDrainsCommandQueue(t *testing.T) {
	sink := newBlockingCommandSink()
	manager, _, channel, _ := openCommandTestTab(t, sink, "")
	if err := channel.emit([]byte("ls\r\n\x1b]133;B\x1b\\\x1b]133;D;0\x1b\\")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("command was not handed to the slow sink")
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the accepted command was flushed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	sink.unblock()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not finish after the sink was released")
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Command != "ls" {
		t.Fatalf("Close did not drain the command tail: %+v", records)
	}
}

func TestAsyncCommandSinkPanicDoesNotStopLaterRecords(t *testing.T) {
	var calls int
	var mu sync.Mutex
	sink := CommandSinkFunc(func(_ context.Context, record CommandRecord) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			panic("injected store panic")
		}
	})
	queue := newAsyncCommandSink(sink, discardCommandLogger(), 2, nil)
	queue.RecordCommand(context.Background(), CommandRecord{SessionID: "session-1", Command: "one"})
	queue.RecordCommand(context.Background(), CommandRecord{SessionID: "session-1", Command: "two"})
	queue.Close()
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || queue.failures.Load() != 1 {
		t.Fatalf("calls=%d failures=%d, want panic isolation and continued drain", calls, queue.failures.Load())
	}
}

func TestTerminalCommandSaturationDoesNotBlockOutputAndPersistsGap(t *testing.T) {
	sink := newBlockingCommandSink()
	manager, tab, channel, _ := openCommandTestTab(t, sink, "")
	t.Cleanup(sink.unblock)
	receiver := bindTestReceiver(t, manager, "cmd-channel")
	payload := []byte("ls\r\n\x1b]133;B\x1b\\\x1b]133;D;0\x1b\\")
	roundTrip := func() {
		t.Helper()
		if err := channel.emit(payload); err != nil {
			t.Fatal(err)
		}
		for {
			frame := receiveTestFrame(t, receiver)
			if frame.Kind != hub.FrameBinary {
				continue
			}
			if !bytes.Equal(frame.Data, payload) {
				t.Fatalf("terminal output changed during command degradation: %q", frame.Data)
			}
			break
		}
	}
	roundTrip()
	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("command worker did not enter the slow sink")
	}

	const extraCommands = commandLogQueueSize + 32
	durations := make([]time.Duration, 0, extraCommands)
	for index := 0; index < extraCommands; index++ {
		started := time.Now()
		roundTrip()
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(len(durations)*99)/100]
	if p99 >= 100*time.Millisecond {
		t.Fatalf("terminal output P99 with a saturated command sink = %s", p99)
	}
	t.Logf("terminal output P99 with a saturated command sink: %s", p99)
	if state := tab.CommandState(); state.Running || !state.HasLastExitCode {
		t.Fatalf("command completion state was blocked by persistence: %+v", state)
	}
	if got := manager.commandQueue.degraded.Load(); got != 32 {
		t.Fatalf("degraded terminal records = %d, want 32", got)
	}

	sink.unblock()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	records := sink.snapshot()
	exact := 0
	gaps := make([]CommandRecord, 0, 1)
	for _, record := range records {
		if record.Source == CommandSourceTerminalGap {
			gaps = append(gaps, record)
		} else {
			exact++
		}
	}
	if exact != commandLogQueueSize+1 || len(gaps) != 1 {
		t.Fatalf("command audit accounting: exact=%d gaps=%d total=%d", exact, len(gaps), len(records))
	}
	if !strings.Contains(gaps[0].Command, "32") || gaps[0].SessionID != tab.SessionID || gaps[0].TabID != tab.ID || gaps[0].ExitCode != nil {
		t.Fatalf("terminal audit gap is not explicit and attributable: %+v", gaps[0])
	}
	if manager.commandQueue.gapRecords.Load() != 1 || manager.commandQueue.failures.Load() != 0 {
		t.Fatalf("unexpected command gap persistence failures: %+v", manager.commandQueue.failures.Load())
	}
}
