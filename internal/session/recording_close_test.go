package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

func TestCloseTabStopsActiveRecordingAndClosesFile(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "recording-close", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	before := descriptorCount(t)
	path := filepath.Join(t.TempDir(), "active.log")
	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}
	if during := descriptorCount(t); during != before+1 {
		t.Fatalf("recording opened %d descriptors, want one", during-before)
	}
	if err := connector.transport(0).channel(0).emit([]byte("active-record")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		count, err := manager.RecordingBytes(tab.ID)
		return err == nil && count >= uint64(len("active-record"))
	})
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if after := descriptorCount(t); after != before {
		t.Fatalf("close left %d recording descriptors open", after-before)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("active-record")) {
		t.Fatalf("closed recording = %q, err = %v", data, err)
	}
}

func TestStopRecordingIncludesFeedVisibleBeforeStop(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "recording-stop", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			frame, err := receiver.Next(ctx)
			cancel()
			if err != nil {
				return
			}
			_ = receiver.Ack(frame.Sequence)
		}
	}()
	path := filepath.Join(t.TempDir(), "stop.log")
	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		marker := []byte(fmt.Sprintf("stop-%06d|", i))
		if err := channel.emit(marker); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !bytes.Contains(tab.terminal.Dump(128), marker) {
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: marker never reached the terminal", i)
			}
		}
		recorded, err := manager.StopRecording(tab.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recorded != uint64(len(marker)) {
			t.Fatalf("iteration %d: stop recorded %d bytes, want %d", i, recorded, len(marker))
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, marker) {
			t.Fatalf("iteration %d: recording = %q, err = %v", i, data, err)
		}
		if err := manager.StartRecording(tab.ID, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStopRecordingPropagatesTerminalError(t *testing.T) {
	stopErr := fmt.Errorf("recording write failed")
	recordingTerminal := &recordingStopTerminal{bytes: 17, err: stopErr}
	manager := NewManager(Config{Terminals: TerminalFactoryFunc(func(TerminalConfig) (TerminalState, error) {
		return recordingTerminal, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	tab, err := manager.RegisterRemoteSession(t.Context(), RemoteSessionOptions{OwnerID: "recording-owner"})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := manager.StopRecording(tab.ID)
	if recorded != 17 || err != stopErr {
		t.Fatalf("stop = (%d, %v), want (17, %v)", recorded, err, stopErr)
	}
}

func TestRecordingStatusPropagatesTerminalState(t *testing.T) {
	want := terminal.RecordingStatus{
		TabID: "recording-tab", State: terminal.RecordingFailed, Bytes: 23,
		Path: "/tmp/recording.log", SafeError: "disk full", Generation: 4,
	}
	recordingTerminal := &recordingStopTerminal{status: want}
	manager := NewManager(Config{Terminals: TerminalFactoryFunc(func(TerminalConfig) (TerminalState, error) {
		return recordingTerminal, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	tab, err := manager.RegisterRemoteSession(t.Context(), RemoteSessionOptions{OwnerID: "recording-owner"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.RecordingStatus(tab.ID)
	if err != nil || got != want {
		t.Fatalf("status = (%+v, %v), want %+v", got, err, want)
	}
	got, err = manager.RecordingStatusFor(requestTestUser("recording-owner"), tab.ID)
	if err != nil || got != want {
		t.Fatalf("owner status = (%+v, %v), want %+v", got, err, want)
	}
}

func TestStartRecordingEmitterCanStopSynchronously(t *testing.T) {
	manager, tabID, recorder := newReentrantRecordingManager(t)
	recorder.startStatus = terminal.RecordingStatus{State: terminal.RecordingRunning, Generation: 1}
	recorder.stopStatus = terminal.RecordingStatus{State: terminal.RecordingStopped, Generation: 1}
	events := make(chan Event, 4)
	var once sync.Once
	var stopErr error
	manager.emitter = EmitterFunc(func(_ context.Context, event Event) error {
		events <- event
		once.Do(func() {
			_, err := manager.StopRecording(tabID)
			stopErr = err
		})
		return nil
	})
	err := runRecordingOperation(t, func() error {
		return manager.StartRecording(tabID, filepath.Join(t.TempDir(), "start.log"))
	})
	if err != nil {
		t.Fatal(err)
	}
	got := waitRecordingEvents(t, events, 2)
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	assertControlVersionsIncrease(t, got)
	select {
	case event := <-events:
		t.Fatalf("unexpected extra event: %+v", event)
	default:
	}
}

func TestStopRecordingEmitterCanStopSynchronously(t *testing.T) {
	manager, tabID, recorder := newReentrantRecordingManager(t)
	recorder.startStatus = terminal.RecordingStatus{State: terminal.RecordingRunning, Generation: 1}
	recorder.stopStatus = terminal.RecordingStatus{State: terminal.RecordingStopped, Generation: 1}
	events := make(chan Event, 2)
	var once sync.Once
	var stopErr error
	var reenter atomic.Bool
	manager.emitter = EmitterFunc(func(_ context.Context, event Event) error {
		events <- event
		if reenter.Load() {
			once.Do(func() {
				_, err := manager.StopRecording(tabID)
				stopErr = err
			})
		}
		return nil
	})
	if err := manager.StartRecording(tabID, filepath.Join(t.TempDir(), "stop.log")); err != nil {
		t.Fatal(err)
	}
	reenter.Store(true)
	if err := runRecordingOperation(t, func() error {
		_, err := manager.StopRecording(tabID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := waitRecordingEvents(t, events, 2)
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	assertControlVersionsIncrease(t, got)
	select {
	case event := <-events:
		t.Fatalf("unexpected extra event: %+v", event)
	default:
	}
}

func TestRecordingWriteFailureEmitterCanStopSynchronously(t *testing.T) {
	manager, tabID, recorder := newReentrantRecordingManager(t)
	writeErr := fmt.Errorf("injected recording write failure")
	recorder.startStatus = terminal.RecordingStatus{State: terminal.RecordingRunning, Generation: 1}
	recorder.feedStatus = terminal.RecordingStatus{State: terminal.RecordingFailed, SafeError: writeErr.Error(), Generation: 1}
	recorder.stopStatus = recorder.feedStatus
	recorder.err = writeErr
	events := make(chan Event, 2)
	var once sync.Once
	var stopErr error
	var reenter atomic.Bool
	manager.emitter = EmitterFunc(func(_ context.Context, event Event) error {
		events <- event
		if reenter.Load() {
			once.Do(func() {
				_, err := manager.StopRecording(tabID)
				stopErr = err
			})
		}
		return nil
	})
	if err := manager.StartRecording(tabID, filepath.Join(t.TempDir(), "write.log")); err != nil {
		t.Fatal(err)
	}
	reenter.Store(true)
	err := runRecordingOperation(t, func() error {
		return manager.InjectInternal(context.Background(), tabID, []byte("output"))
	})
	if err != nil {
		t.Fatal(err)
	}
	got := waitRecordingEvents(t, events, 2)
	if !errors.Is(stopErr, writeErr) {
		t.Fatalf("reentrant stop error = %v, want %v", stopErr, writeErr)
	}
	assertControlVersionsIncrease(t, got)
	if err := manager.InjectInternal(context.Background(), tabID, []byte("more")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		t.Fatalf("duplicate failure event: %+v", event)
	default:
	}
}

func TestRecordingFailureAndCwdControlVersionsStayOrdered(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)
	manager, tab, recorder := newRecordingSSHManager(t)
	recorder.startStatus = terminal.RecordingStatus{State: terminal.RecordingRunning, Generation: 1}
	recorder.feedStatus = terminal.RecordingStatus{State: terminal.RecordingFailed, SafeError: "injected recording write failure", Generation: 1}
	recorder.err = errors.New(recorder.feedStatus.SafeError)
	events := make(chan Event, 4)
	manager.emitter = EmitterFunc(func(_ context.Context, event Event) error {
		events <- event
		return nil
	})
	if err := manager.StartRecording(tab.ID, filepath.Join(t.TempDir(), "cwd.log")); err != nil {
		t.Fatal(err)
	}
	osc := "\x1b]7;file://buildhost/tmp/next\x1b\\"
	if err := manager.InjectInternal(context.Background(), tab.ID, []byte(osc)); err != nil {
		t.Fatal(err)
	}
	got := waitRecordingEvents(t, events, 3)
	if got[0].Payload.(ControlEvent).Cwd != "" || got[1].Payload.(ControlEvent).Cwd != "" {
		t.Fatalf("CWD event overtook recording lifecycle: %+v", got)
	}
	if got[2].Payload.(ControlEvent).Cwd != "/tmp/next" {
		t.Fatalf("CWD event is not ordered after recording events: %+v", got)
	}
	assertControlVersionsIncrease(t, got)
}

func TestRecordingOperationAndResizeControlVersionsStayOrdered(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)
	manager, tab, recorder := newRecordingSSHManager(t)
	recorder.startStatus = terminal.RecordingStatus{State: terminal.RecordingRunning, Generation: 1}
	recorder.feedStatus = terminal.RecordingStatus{State: terminal.RecordingFailed, SafeError: "injected recording write failure", Generation: 1}
	recorder.err = errors.New(recorder.feedStatus.SafeError)
	events := make(chan Event, 4)
	manager.emitter = EmitterFunc(func(_ context.Context, event Event) error {
		events <- event
		return nil
	})
	if err := manager.StartRecording(tab.ID, filepath.Join(t.TempDir(), "resize.log")); err != nil {
		t.Fatal(err)
	}
	if err := manager.InjectInternal(context.Background(), tab.ID, []byte("recording failure")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resize(context.Background(), tab.ID, "client-a", 100, 30); err != nil {
		t.Fatal(err)
	}
	got := waitRecordingEvents(t, events, 3)
	if got[0].Payload.(ControlEvent).GridRevision != 0 || got[1].Payload.(ControlEvent).GridRevision != 0 || got[2].Payload.(ControlEvent).GridRevision != 1 {
		t.Fatalf("resize event overtook recording operation: %+v", got)
	}
	assertControlVersionsIncrease(t, got)
}

func TestRecordingOperationsAndMetadataControlVersionsStayOrdered(t *testing.T) {
	connector := newFakeConnector()
	var eventsMu sync.Mutex
	var versions []uint64
	manager := NewManager(Config{
		Connector: connector,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if event.Topic == TopicTerminalControl {
				control := event.Payload.(ControlEvent)
				eventsMu.Lock()
				versions = append(versions, control.Version)
				eventsMu.Unlock()
			}
			return nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "recording-concurrent-control", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	path := filepath.Join(t.TempDir(), "concurrent.log")
	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}

	const rounds = 24
	errs := make(chan error, rounds*3)
	var wg sync.WaitGroup
	run := func(operation func(int) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < rounds; index++ {
				if err := operation(index); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	run(func(int) error {
		if _, err := manager.StopRecording(tab.ID); err != nil {
			return err
		}
		return manager.StartRecording(tab.ID, path)
	})
	run(func(index int) error {
		_, err := manager.RenameTab(context.Background(), tab.ID, fmt.Sprintf("recording-%02d", index))
		return err
	})
	run(func(index int) error {
		return manager.Resize(context.Background(), tab.ID, "client-a", uint32(90+index%5), uint32(30+index%4))
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	queue := recordingControlOrderForTab(tab)
	waitFor(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return !queue.scheduled.Load() && len(queue.pending) == 0
	})
	eventsMu.Lock()
	got := append([]uint64(nil), versions...)
	eventsMu.Unlock()
	for index := 1; index < len(got); index++ {
		if got[index] <= got[index-1] {
			t.Fatalf("control versions are not strictly increasing: %v", got)
		}
	}
}

func newRecordingSSHManager(t *testing.T) (*Manager, *Tab, *recordingStopTerminal) {
	t.Helper()
	connector := newFakeConnector()
	recorder := &recordingStopTerminal{}
	manager := NewManager(Config{
		Connector: connector,
		Terminals: TerminalFactoryFunc(func(TerminalConfig) (TerminalState, error) {
			return recorder, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "recording-control-order", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	return manager, tab, recorder
}

func newReentrantRecordingManager(t *testing.T) (*Manager, string, *recordingStopTerminal) {
	t.Helper()
	recorder := &recordingStopTerminal{}
	manager := NewManager(Config{Terminals: TerminalFactoryFunc(func(TerminalConfig) (TerminalState, error) {
		return recorder, nil
	})})
	tab, err := manager.RegisterRemoteSession(t.Context(), RemoteSessionOptions{OwnerID: "recording-owner"})
	if err != nil {
		t.Fatal(err)
	}
	return manager, tab.ID, recorder
}

func runRecordingOperation(t *testing.T, operation func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- operation()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("recording operation deadlocked")
		return nil
	}
}

func waitRecordingEvents(t *testing.T, events <-chan Event, count int) []Event {
	t.Helper()
	got := make([]Event, 0, count)
	for len(got) < count {
		select {
		case event := <-events:
			if event.Topic == TopicTerminalControl {
				got = append(got, event)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("received %d events, want %d: %+v", len(got), count, got)
		}
	}
	return got
}

func assertControlVersionsIncrease(t *testing.T, events []Event) {
	t.Helper()
	var previous uint64
	for i, event := range events {
		control, ok := event.Payload.(ControlEvent)
		if !ok {
			t.Fatalf("event %d payload = %T, want ControlEvent", i, event.Payload)
		}
		if control.Version <= previous {
			t.Fatalf("event versions are not ordered: %+v", events)
		}
		previous = control.Version
	}
}

type recordingStopTerminal struct {
	bytes           uint64
	err             error
	status          terminal.RecordingStatus
	handler         func(terminal.RecordingStatus)
	startStatus     terminal.RecordingStatus
	stopStatus      terminal.RecordingStatus
	feedStatus      terminal.RecordingStatus
	stopNotified    bool
	failureNotified bool
	startCalls      int
	stopCalls       int
	feedCalls       int
}

func (t *recordingStopTerminal) StartRecording(string) error {
	t.startCalls++
	t.stopNotified = false
	t.failureNotified = false
	if t.startStatus.State != "" {
		t.status = t.startStatus
		t.emit(t.status)
	}
	return nil
}

func (t *recordingStopTerminal) StopRecording() (uint64, error) {
	t.stopCalls++
	switch t.stopStatus.State {
	case terminal.RecordingFailed:
		if !t.failureNotified {
			t.failureNotified = true
			t.status = t.stopStatus
			t.emit(t.status)
		}
	case "":
	default:
		if !t.stopNotified {
			t.stopNotified = true
			t.status = t.stopStatus
			t.emit(t.status)
		}
	}
	return t.bytes, t.err
}

func (t *recordingStopTerminal) RecordingStatus() terminal.RecordingStatus { return t.status }

func (t *recordingStopTerminal) SetRecordingHandler(handler func(terminal.RecordingStatus)) {
	t.handler = handler
}

func (t *recordingStopTerminal) emit(status terminal.RecordingStatus) {
	if t.handler != nil {
		t.handler(status)
	}
}

func (t *recordingStopTerminal) Feed([]byte) {
	t.feedCalls++
	if t.feedStatus.State == terminal.RecordingFailed && !t.failureNotified {
		t.failureNotified = true
		t.status = t.feedStatus
		t.emit(t.status)
	}
}

func (t *recordingStopTerminal) Dump(int) []byte                 { return nil }
func (t *recordingStopTerminal) Resize(int, int) error           { return nil }
func (t *recordingStopTerminal) SetResponseHandler(func([]byte)) {}
func (t *recordingStopTerminal) Close()                          {}

func descriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := filepath.Glob("/dev/fd/*")
	if err != nil {
		t.Skipf("open descriptor counting unavailable: %v", err)
	}
	return len(entries)
}
