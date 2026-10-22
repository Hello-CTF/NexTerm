package execbatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeStreamTransport struct {
	generation uint64
	open       func(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error)
	exec       func(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error)
}

func (f *fakeStreamTransport) Kind() string       { return "ssh" }
func (f *fakeStreamTransport) Generation() uint64 { return f.generation }
func (f *fakeStreamTransport) IsAlive() bool      { return true }
func (f *fakeStreamTransport) Close() error       { return nil }
func (f *fakeStreamTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}

func (f *fakeStreamTransport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	return f.exec(ctx, command, options)
}

func (f *fakeStreamTransport) OpenExec(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
	return f.open(ctx, command, options)
}

type fakeStreamChannel struct {
	stdout  *io.PipeReader
	stderr  *io.PipeReader
	done    chan struct{}
	waitErr error
}

func (c *fakeStreamChannel) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *fakeStreamChannel) Stderr() io.Reader           { return c.stderr }
func (c *fakeStreamChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *fakeStreamChannel) CloseWrite() error           { return nil }
func (c *fakeStreamChannel) Resize(context.Context, uint32, uint32) error {
	return nil
}
func (c *fakeStreamChannel) ID() string         { return "fake" }
func (c *fakeStreamChannel) Generation() uint64 { return 1 }
func (c *fakeStreamChannel) Wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close is a no-op: the script goroutine always closes the pipe writers, so
// readers observe EOF (or the context error) without local interruption.
func (c *fakeStreamChannel) Close() error { return nil }

type fakeHostScript struct {
	stdout  []string
	stderr  []string
	waitErr error
	block   bool
}

func (s fakeHostScript) open(ctx context.Context, command string, options base.ExecOptions) (base.Channel, error) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	channel := &fakeStreamChannel{stdout: stdoutR, stderr: stderrR, done: make(chan struct{}), waitErr: s.waitErr}
	go func() {
		defer close(channel.done)
		if s.block {
			<-ctx.Done()
			_ = stdoutW.CloseWithError(ctx.Err())
			_ = stderrW.CloseWithError(ctx.Err())
			return
		}
		for index := 0; index < len(s.stdout) || index < len(s.stderr); index++ {
			if index < len(s.stdout) {
				_, _ = stdoutW.Write([]byte(s.stdout[index]))
			}
			if index < len(s.stderr) {
				_, _ = stderrW.Write([]byte(s.stderr[index]))
			}
		}
		_ = stdoutW.Close()
		_ = stderrW.Close()
	}()
	return channel, nil
}

func streamTestTarget(sessionID string, script fakeHostScript) Target {
	return Target{
		SessionID: sessionID, SessionName: sessionID, Transport: &fakeStreamTransport{
			generation: 7,
			open:       script.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot exec not scripted")
			},
		},
		Generation: 7,
	}
}

func requireStreamEventOrder(t *testing.T, tracker *ProgressTracker, sessionID string, wantFirst, wantLast ProgressEventKind) {
	t.Helper()
	var kinds []ProgressEventKind
	for _, event := range tracker.Events(0) {
		if event.Target != nil && event.Target.SessionID == sessionID {
			kinds = append(kinds, event.Kind)
		}
	}
	if len(kinds) == 0 || kinds[0] != wantFirst || kinds[len(kinds)-1] != wantLast {
		t.Fatalf("host %s event order = %+v", sessionID, kinds)
	}
	for index := 1; index < len(kinds); index++ {
		if kinds[index-1] == ProgressEventTargetFinished {
			t.Fatalf("host %s has events after finish: %+v", sessionID, kinds)
		}
	}
}

func TestExecuteTargetStreamSuccess(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{
		stdout: []string{"hello ", "世界", " tail"},
		stderr: []string{"warn ", "more"},
	})
	result, err := ExecuteTargetStream(context.Background(), tracker, target, "true", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSuccess || result.ExitCode == nil || *result.ExitCode != 0 || result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	_, targets := tracker.Snapshot()
	if targets[0].Output.Stdout.Head != "hello 世界 tail" || targets[0].Output.Stderr.Head != "warn more" {
		t.Fatalf("retained output = %+v", targets[0].Output)
	}
	if !utf8.ValidString(targets[0].Output.Stdout.Head) {
		t.Fatal("retained output is not valid UTF-8")
	}
	batch, _ := tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished || batch.Succeeded != 1 {
		t.Fatalf("batch = %+v", batch)
	}
	requireStreamEventOrder(t, tracker, "host-0", ProgressEventTargetStarted, ProgressEventTargetFinished)
}

func TestExecuteTargetStreamExitError(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{
		stdout:  []string{"partial"},
		waitErr: &base.ExitError{Code: 2, Signal: "TERM"},
	})
	result, err := ExecuteTargetStream(context.Background(), tracker, target, "false", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusExitError || result.ExitCode == nil || *result.ExitCode != 2 || result.Signal != "TERM" {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecuteTargetStreamTransportError(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{waitErr: errors.New("connection reset")})
	result, err := ExecuteTargetStream(context.Background(), tracker, target, "cmd", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusTransportError || result.Error != "connection reset" {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecuteTargetStreamCanceled(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{block: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		result, err := ExecuteTargetStream(ctx, tracker, target, "hang", TargetStreamOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	_, targets := tracker.Snapshot()
	for targets[0].Phase != ProgressPhaseRunning {
		time.Sleep(time.Millisecond)
		_, targets = tracker.Snapshot()
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != StatusCanceled {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled stream did not finish")
	}
}

func TestExecuteTargetStreamTimeout(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{block: true})
	result, err := ExecuteTargetStream(context.Background(), tracker, target, "hang", TargetStreamOptions{Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusTimeout {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecuteTargetStreamOneShotFallback(t *testing.T) {
	exitCode := 3
	target := Target{
		SessionID: "host-0", SessionName: "host-0",
		Transport: &oneShotTransport{result: base.ExecResult{Stdout: "out", Stderr: "err", ExitCode: &exitCode}},
	}
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	result, err := ExecuteTargetStream(context.Background(), tracker, target, "cmd", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusExitError || result.ExitCode == nil || *result.ExitCode != 3 {
		t.Fatalf("result = %+v", result)
	}
	_, targets := tracker.Snapshot()
	if targets[0].Output.Stdout.Head != "out" || targets[0].Output.Stderr.Head != "err" {
		t.Fatalf("forwarded output = %+v", targets[0].Output)
	}

	target = Target{
		SessionID: "host-0", SessionName: "host-0",
		Transport: &oneShotTransport{result: base.ExecResult{Stdout: "out"}},
	}
	tracker = NewProgressTracker("batch-1", []string{"host-0"})
	result, err = ExecuteTargetStream(context.Background(), tracker, target, "cmd", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusTransportError || result.Error != base.ErrExitStatusMissing.Error() {
		t.Fatalf("missing exit status result = %+v", result)
	}
}

type oneShotTransport struct {
	result base.ExecResult
	err    error
}

func (o *oneShotTransport) Kind() string       { return "ssh" }
func (o *oneShotTransport) Generation() uint64 { return 1 }
func (o *oneShotTransport) IsAlive() bool      { return true }
func (o *oneShotTransport) Close() error       { return nil }
func (o *oneShotTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (o *oneShotTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return o.result, o.err
}

func TestExecuteTargetStreamValidation(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{})

	if _, err := ExecuteTargetStream(context.Background(), nil, target, "cmd", TargetStreamOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil tracker = %v", err)
	}
	if _, err := ExecuteTargetStream(context.Background(), tracker, target, "", TargetStreamOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty command = %v", err)
	}
	if _, err := ExecuteTargetStream(context.Background(), tracker, Target{SessionID: "host-0"}, "cmd", TargetStreamOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nil transport = %v", err)
	}
	if _, err := ExecuteTargetStream(context.Background(), tracker, target, "cmd", TargetStreamOptions{}); err != nil {
		t.Fatalf("stream target = %v", err)
	}
	unknown := streamTestTarget("missing", fakeHostScript{})
	if _, err := ExecuteTargetStream(context.Background(), tracker, unknown, "cmd", TargetStreamOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown target = %v", err)
	}
}

func TestExecuteTargetStreamBoundedRetention(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := streamTestTarget("host-0", fakeHostScript{
		stdout: []string{strings.Repeat("a", 128<<10)},
		stderr: []string{strings.Repeat("e", 128<<10)},
	})
	result, err := ExecuteTargetStream(context.Background(), tracker, target, "big", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	_, targets := tracker.Snapshot()
	stdout := targets[0].Output.Stdout
	if stdout.TotalBytes != 128<<10 || !stdout.Truncated || stdout.DroppedBytes != stdout.TotalBytes-len(stdout.Head)-len(stdout.Tail) {
		t.Fatalf("stdout retention = %+v", stdout)
	}
}

// TestExecBatchTenHostFakeTransportAcceptance is the R-10 acceptance over
// the real streaming driver with a deterministic in-process fake transport:
// interleaved UTF-8 output, bounded head/tail retention, timeout,
// cancellation, and failed-only rerun wiring. No real SSH hosts are
// involved; live 10-host verification remains with the M15 service wiring.
func TestExecBatchTenHostFakeTransportAcceptance(t *testing.T) {
	const hosts = 10
	batchCtx, cancelBatch := context.WithCancel(context.Background())
	defer cancelBatch()
	now := time.Unix(1_700_000_000, 0)

	sessionIDs := make([]string, hosts)
	targets := make([]Target, hosts)
	timeouts := make([]time.Duration, hosts)
	for host := 0; host < hosts; host++ {
		sessionIDs[host] = fmt.Sprintf("session-%02d", host)
		marker := fmt.Sprintf("host-%02d ", host)
		script := fakeHostScript{
			stdout:  []string{marker + strings.Repeat("o", 64<<10), strings.Repeat("中", 4096), " END-" + fmt.Sprintf("%02d", host)},
			stderr:  []string{marker + "warn"},
			waitErr: nil,
		}
		switch host {
		case 2:
			script.waitErr = &base.ExitError{Code: 1}
		case 5:
			script.waitErr = errors.New("transport exploded")
		case 7:
			script.block = true
			timeouts[host] = 40 * time.Millisecond
		case 9:
			script.block = true
		}
		targets[host] = streamTestTarget(sessionIDs[host], script)
		targets[host].SessionName = sessionIDs[host]
	}

	tracker := NewProgressTracker("batch-acceptance", sessionIDs)
	results := make([]Result, hosts)
	var workers sync.WaitGroup
	for host := 0; host < hosts; host++ {
		workers.Add(1)
		go func(host int) {
			defer workers.Done()
			options := TargetStreamOptions{Now: func() time.Time { return now }}
			if timeouts[host] > 0 {
				options.Timeout = timeouts[host]
			}
			result, err := ExecuteTargetStream(batchCtx, tracker, targets[host], "deploy", options)
			if err != nil {
				t.Error(err)
				return
			}
			results[host] = result
		}(host)
	}
	// host 9 hangs until the batch is canceled, mirroring the service cancel
	// contract; the other nine finish first.
	waitNine := make(chan struct{})
	go func() {
		defer close(waitNine)
		for {
			batch, _ := tracker.Snapshot()
			if batch.Succeeded+batch.Failed >= 9 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-waitNine:
	case <-time.After(10 * time.Second):
		t.Fatal("nine hosts did not finish")
	}
	cancelBatch()
	workers.Wait()

	batch, progress := tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished || batch.Total != hosts ||
		batch.Succeeded != 6 || batch.Failed != 3 || batch.Canceled != 1 {
		t.Fatalf("rollup = %+v", batch)
	}
	wantStatuses := []Status{StatusSuccess, StatusSuccess, StatusExitError, StatusSuccess, StatusSuccess, StatusTransportError, StatusSuccess, StatusTimeout, StatusSuccess, StatusCanceled}
	for host, target := range progress {
		if target.Status != wantStatuses[host] {
			t.Fatalf("host %d status = %s, want %s", host, target.Status, wantStatuses[host])
		}
		if target.StartedAt == 0 || target.FinishedAt == 0 {
			t.Fatalf("host %d timing = %+v", host, target)
		}
		requireStreamEventOrder(t, tracker, target.SessionID, ProgressEventTargetStarted, ProgressEventTargetFinished)
		if host == 7 || host == 9 {
			continue
		}
		stdout := target.Output.Stdout
		if !strings.HasPrefix(stdout.Head, fmt.Sprintf("host-%02d ", host)) || !strings.HasSuffix(stdout.Tail, fmt.Sprintf("END-%02d", host)) {
			t.Fatalf("host %d head/tail = %q/%q", host, stdout.Head, stdout.Tail)
		}
		if !utf8.ValidString(stdout.Head) || !utf8.ValidString(stdout.Tail) {
			t.Fatalf("host %d retained invalid UTF-8", host)
		}
		if !stdout.Truncated || stdout.TotalBytes == 0 {
			t.Fatalf("host %d retention = %+v", host, stdout)
		}
	}

	rerun := SelectRerunTargets(results)
	if strings.Join(rerun, ",") != "session-02,session-05,session-07" {
		t.Fatalf("rerun selection = %+v", rerun)
	}
	original := Request{
		BatchID: "batch-acceptance", Command: "deploy", TimeoutMS: 30000, MaxConcurrent: 4,
		ProductionConfirmation: &ProductionConfirmation{Confirmed: true},
	}
	executor := &fakeBatchExecutor{}
	finished, err := ExecuteRerun(context.Background(), executor, "owner-a", original, results, "batch-rerun")
	if err != nil {
		t.Fatal(err)
	}
	if finished.BatchID != "rerun-done" {
		t.Fatalf("executor batch = %+v", finished)
	}
	request := executor.request
	if request.BatchID != "batch-rerun" || request.Command != "deploy" || request.TimeoutMS != 30000 || request.MaxConcurrent != 4 {
		t.Fatalf("rerun request = %+v", request)
	}
	if strings.Join(request.SessionIDs, ",") != "session-02,session-05,session-07" {
		t.Fatalf("rerun sessions = %+v", request.SessionIDs)
	}
	if request.ProductionConfirmation != original.ProductionConfirmation {
		t.Fatal("rerun request dropped the caller's fresh production confirmation")
	}
	if request.Runbook != nil {
		t.Fatal("rerun request kept the runbook metadata")
	}
}

type fakeBatchExecutor struct {
	request Request
}

func (f *fakeBatchExecutor) Execute(_ context.Context, ownerID string, request Request) (Batch, error) {
	if ownerID != "owner-a" {
		return Batch{}, errors.New("wrong owner")
	}
	f.request = request
	return Batch{BatchID: "rerun-done"}, nil
}
