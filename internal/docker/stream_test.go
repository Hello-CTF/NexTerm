package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type collectSink struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	frames  chan struct{}
	started chan struct{}
	block   bool
	once    sync.Once
}

func newCollectSink() *collectSink {
	return &collectSink{frames: make(chan struct{}, 32), started: make(chan struct{})}
}

func (s *collectSink) Send(ctx context.Context, frame []byte) error {
	s.once.Do(func() { close(s.started) })
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	s.mu.Lock()
	_, _ = s.buffer.Write(frame)
	s.mu.Unlock()
	select {
	case s.frames <- struct{}{}:
	default:
	}
	return nil
}

func (s *collectSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.String()
}

func (s *collectSink) waitFrame(t *testing.T) {
	t.Helper()
	select {
	case <-s.frames:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream frame")
	}
}

type pipeExecSession struct {
	reader     *io.PipeReader
	writer     *io.PipeWriter
	input      bytes.Buffer
	inputMu    sync.Mutex
	resizeMu   sync.Mutex
	resizes    [][2]uint
	closed     atomic.Bool
	closeWrite atomic.Int32
	tty        bool
	exitCode   int
}

func newPipeExecSession(tty bool) *pipeExecSession {
	reader, writer := io.Pipe()
	return &pipeExecSession{reader: reader, writer: writer, tty: tty}
}

func (s *pipeExecSession) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *pipeExecSession) Write(p []byte) (int, error) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	return s.input.Write(p)
}
func (s *pipeExecSession) Close() error {
	s.closed.Store(true)
	_ = s.writer.Close()
	return s.reader.Close()
}
func (s *pipeExecSession) CloseWrite() error {
	s.closeWrite.Add(1)
	return nil
}
func (s *pipeExecSession) Resize(_ context.Context, width, height uint) error {
	s.resizeMu.Lock()
	s.resizes = append(s.resizes, [2]uint{width, height})
	s.resizeMu.Unlock()
	return nil
}
func (s *pipeExecSession) Wait(context.Context) (int, error) { return s.exitCode, nil }
func (s *pipeExecSession) IsTTY() bool                       { return s.tty }
func (s *pipeExecSession) emit(t *testing.T, value string) {
	t.Helper()
	go func() { _, _ = io.WriteString(s.writer, value) }()
}

func waitExecReplay(t *testing.T, service *Service, id string, size int) {
	t.Helper()
	stream, err := service.streams.get(id)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stream.mu.Lock()
		got := stream.replay.size
		stream.mu.Unlock()
		if got == size {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d replay bytes", size)
}

func TestAttachExecLifecycleDetachResumeResize(t *testing.T) {
	exec := newPipeExecSession(true)
	backend := &stubBackend{openExec: func(_ context.Context, options ExecOptions) (ExecSession, error) {
		if options.Width != 20 || options.Height != 5 || !options.TTY {
			t.Fatalf("unexpected initial exec options: %+v", options)
		}
		return exec, nil
	}}
	service := NewService(&stubProvider{sdk: backend})
	first := newCollectSink()
	id, err := service.AttachExec(t.Context(), ExecAttachRequest{SessionID: "s1", Container: "c1", Width: 10, Height: 2, Sink: first})
	if err != nil {
		t.Fatal(err)
	}
	done, err := service.StreamDone(id)
	if err != nil {
		t.Fatal(err)
	}
	exec.emit(t, "one")
	first.waitFrame(t)
	if first.String() != "one" {
		t.Fatalf("first sink = %q", first.String())
	}
	if err := service.WriteExec(t.Context(), id, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	exec.inputMu.Lock()
	input := exec.input.String()
	exec.inputMu.Unlock()
	if input != "abc" {
		t.Fatalf("exec input = %q", input)
	}
	if err := service.ResizeExec(t.Context(), id, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	if exec.closed.Load() {
		t.Fatal("detach killed exec")
	}
	exec.emit(t, "two")
	waitExecReplay(t, service, id, 3)
	second := newCollectSink()
	if err := service.ResumeExec(id, second); err != nil {
		t.Fatal(err)
	}
	second.waitFrame(t)
	exec.emit(t, "three")
	second.waitFrame(t)
	if second.String() != "twothree" || first.String() != "one" {
		t.Fatalf("resume output first=%q second=%q", first.String(), second.String())
	}
	if err := service.CloseSession("s1"); err != nil {
		t.Fatal(err)
	}
	if !exec.closed.Load() {
		t.Fatal("session close did not close exec")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("exec done was not signaled")
	}
	if _, ok := <-done; ok {
		t.Fatal("exec exit was signaled more than once")
	}
	exec.resizeMu.Lock()
	defer exec.resizeMu.Unlock()
	if len(exec.resizes) != 1 || exec.resizes[0] != [2]uint{100, 30} {
		t.Fatalf("resizes = %v", exec.resizes)
	}
}

func TestCompletedDetachedExecRetainsReplayAndExit(t *testing.T) {
	exec := newPipeExecSession(true)
	exec.exitCode = 23
	backend := &stubBackend{openExec: func(context.Context, ExecOptions) (ExecSession, error) { return exec, nil }}
	service := NewService(&stubProvider{sdk: backend})
	id, err := service.AttachExec(t.Context(), ExecAttachRequest{SessionID: "s1", Container: "c1", Sink: newCollectSink()})
	if err != nil {
		t.Fatal(err)
	}
	done, err := service.StreamDone(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	exec.emit(t, "final")
	waitExecReplay(t, service, id, 5)
	_ = exec.writer.Close()
	select {
	case exit := <-done:
		if exit.ExitCode != 23 || exit.Err != nil {
			t.Fatalf("exit = %+v", exit)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec did not complete")
	}
	status, completed, err := service.StreamStatus(id)
	if err != nil || !completed || status.ExitCode != 23 {
		t.Fatalf("status = %+v, completed = %v, err = %v", status, completed, err)
	}
	resumed := newCollectSink()
	if err := service.ResumeExec(id, resumed); err != nil {
		t.Fatal(err)
	}
	resumed.waitFrame(t)
	if resumed.String() != "final" {
		t.Fatalf("completed replay = %q", resumed.String())
	}
	if err := service.WriteExec(t.Context(), id, []byte("x")); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("write after completion = %v", err)
	}
	if err := service.CloseStream(id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.StreamStatus(id); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("status after explicit cleanup = %v", err)
	}
}

func TestExecReplayIsBounded(t *testing.T) {
	exec := newPipeExecSession(true)
	backend := &stubBackend{openExec: func(context.Context, ExecOptions) (ExecSession, error) { return exec, nil }}
	service := NewService(&stubProvider{sdk: backend}, WithConfig(Config{ExecReplayBytes: 4}))
	id, err := service.AttachExec(t.Context(), ExecAttachRequest{SessionID: "s1", Container: "c1", Sink: newCollectSink()})
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseStream(id)
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	exec.emit(t, "abcdefgh")
	waitExecReplay(t, service, id, 4)
	resumed := newCollectSink()
	if err := service.ResumeExec(id, resumed); err != nil {
		t.Fatal(err)
	}
	resumed.waitFrame(t)
	if resumed.String() != "efgh" {
		t.Fatalf("bounded replay = %q", resumed.String())
	}
}

func TestExecResumeBackpressureCanBeClosed(t *testing.T) {
	exec := newPipeExecSession(true)
	backend := &stubBackend{openExec: func(context.Context, ExecOptions) (ExecSession, error) { return exec, nil }}
	service := NewService(&stubProvider{sdk: backend})
	id, err := service.AttachExec(t.Context(), ExecAttachRequest{SessionID: "s1", Container: "c1", Sink: newCollectSink()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	exec.emit(t, "backlog")
	waitExecReplay(t, service, id, 7)
	resumed := newCollectSink()
	resumed.block = true
	resumeResult := make(chan error, 1)
	go func() { resumeResult <- service.ResumeExec(id, resumed) }()
	select {
	case <-resumed.started:
	case <-time.After(2 * time.Second):
		t.Fatal("replay did not reach blocked sink")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- service.CloseStream(id) }()
	select {
	case err := <-resumeResult:
		if err == nil {
			t.Fatal("blocked resume unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not interrupt replay")
	}
	select {
	case <-closeResult:
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked behind replay")
	}
}

func TestAttachLogsDistinguishesOmittedAndZeroTail(t *testing.T) {
	tails := make(chan int, 2)
	backend := &stubBackend{openLogs: func(_ context.Context, options LogsOptions) (LogStream, error) {
		tails <- options.Tail
		return LogStream{Reader: io.NopCloser(bytes.NewReader(nil)), TTY: true}, nil
	}}
	service := NewService(&stubProvider{sdk: backend})
	if _, err := service.AttachLogs(t.Context(), LogsAttachRequest{SessionID: "s1", Container: "c1", Sink: newCollectSink()}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := service.AttachLogs(t.Context(), LogsAttachRequest{SessionID: "s1", Container: "c1", Tail: &zero, Sink: newCollectSink()}); err != nil {
		t.Fatal(err)
	}
	if got := <-tails; got != 500 {
		t.Fatalf("omitted tail = %d", got)
	}
	if got := <-tails; got != 0 {
		t.Fatalf("explicit zero tail = %d", got)
	}
}

func TestAttachLogsDemultiplexesAndDetachCloses(t *testing.T) {
	reader, writer := io.Pipe()
	backend := &stubBackend{openLogs: func(context.Context, LogsOptions) (LogStream, error) {
		return LogStream{Reader: reader, TTY: false}, nil
	}}
	service := NewService(&stubProvider{sdk: backend})
	sink := newCollectSink()
	id, err := service.AttachLogs(t.Context(), LogsAttachRequest{SessionID: "s1", Container: "c1", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	done, err := service.StreamDone(id)
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{1, 0, 0, 0, 0, 0, 0, 4}, []byte("out\n")...)
	payload = append(payload, []byte{2, 0, 0, 0, 0, 0, 0, 4}...)
	payload = append(payload, []byte("err\n")...)
	go func() {
		for _, value := range payload {
			_, _ = writer.Write([]byte{value})
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for sink.String() != "out\nerr\n" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.String() != "out\nerr\n" {
		t.Fatalf("demultiplexed logs = %q", sink.String())
	}
	if err := service.DetachStream(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logs detach did not clean up")
	}
}

func TestLogsBlockedSinkDoesNotPreventCleanup(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	backend := &stubBackend{openLogs: func(context.Context, LogsOptions) (LogStream, error) {
		return LogStream{Reader: reader, TTY: true}, nil
	}}
	service := NewService(&stubProvider{sdk: backend})
	sink := newCollectSink()
	sink.block = true
	id, err := service.AttachLogs(t.Context(), LogsAttachRequest{SessionID: "s1", Container: "c1", Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.WriteString(writer, "block") }()
	select {
	case <-sink.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sink was not called")
	}
	closed := make(chan error, 1)
	go func() { closed <- service.CloseStream(id) }()
	select {
	case err := <-closed:
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked sink prevented close")
	}
}

func TestAttachCannotResurrectAfterSessionClose(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &stubBackend{openLogs: func(context.Context, LogsOptions) (LogStream, error) {
		close(started)
		<-release
		return LogStream{Reader: reader, TTY: true}, nil
	}}
	service := NewService(&stubProvider{sdk: backend})
	result := make(chan error, 1)
	go func() {
		_, err := service.AttachLogs(context.Background(), LogsAttachRequest{SessionID: "s1", Container: "c1", Sink: newCollectSink()})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("attach did not start")
	}
	if err := service.CloseSession("s1"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrStreamClosed) {
			t.Fatalf("stale attach error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale attach did not finish")
	}
	service.streams.mu.Lock()
	remaining := len(service.streams.streams)
	service.streams.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("stale stream resurrected: %d entries", remaining)
	}
}

func TestCustomExecAlwaysUsesCommandBackendVerbatim(t *testing.T) {
	exec := newPipeExecSession(true)
	opener := &recordingOpener{stream: &commandStreamAdapter{pipeExecSession: exec}}
	command := NewCommandBackend(&recordingRunner{}, opener, ShellPOSIX)
	var sdkOpens atomic.Int32
	sdk := &stubBackend{openExec: func(context.Context, ExecOptions) (ExecSession, error) {
		sdkOpens.Add(1)
		return nil, errors.New("must not be called")
	}}
	service := NewService(&stubProvider{sdk: sdk, command: command})
	custom := "printf 'a  b'; exec sh"
	id, err := service.AttachExec(t.Context(), ExecAttachRequest{SessionID: "s1", CustomCmd: custom, Sink: newCollectSink()})
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseStream(id)
	if len(opener.commands) != 1 || opener.commands[0] != custom || sdkOpens.Load() != 0 {
		t.Fatalf("commands = %q, sdk opens = %d", opener.commands, sdkOpens.Load())
	}
}

type commandStreamAdapter struct {
	*pipeExecSession
}

func (s *commandStreamAdapter) Wait(context.Context) (int, error) { return 0, nil }
