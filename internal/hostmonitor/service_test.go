package hostmonitor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeRunner struct {
	calls     atomic.Int32
	withDisks atomic.Int32
	reply     func(ctx context.Context, call int32) (base.ExecResult, error)
}

func (f *fakeRunner) Exec(ctx context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	switch command {
	case SampleCommand(true):
		f.withDisks.Add(1)
	case SampleCommand(false):
	default:
		return base.ExecResult{}, errors.New("unexpected command")
	}
	return f.reply(ctx, f.calls.Add(1))
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestService(t *testing.T, runner Runner) (*Service, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	service := NewService(func(_ context.Context, sessionID string) (Target, error) {
		if sessionID == "foreign" {
			return Target{}, ipc.NewError(ipc.CodeForbidden, "forbidden")
		}
		return Target{Runner: runner}, nil
	})
	service.now = clock.Now
	return service, clock
}

func sampleReplies(t *testing.T, files ...string) func(context.Context, int32) (base.ExecResult, error) {
	outputs := make([]string, len(files))
	for i, file := range files {
		outputs[i] = readSample(t, file)
	}
	return func(_ context.Context, call int32) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{Stdout: outputs[min(int(call), len(outputs))-1], ExitCode: &code}, nil
	}
}

func TestSampleComputesDeltasAcrossCalls(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "linux_ubuntu_1.txt", "linux_ubuntu_2.txt")}
	service, clock := newTestService(t, runner)

	first, err := service.Sample(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusOK || first.CPUPercent != nil || first.Memory == nil || first.IntervalMs != DefaultInterval.Milliseconds() || first.SampledAt == 0 {
		t.Fatalf("first = %+v", first)
	}
	clock.Advance(DefaultInterval)
	second, err := service.Sample(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUPercent == nil || second.Network[0].RxRate == nil || runner.calls.Load() != 2 {
		t.Fatalf("second = %+v after %d calls", second, runner.calls.Load())
	}
}

func TestSampleSharesResultWithinMinSpacing(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "linux_ubuntu_1.txt", "linux_ubuntu_2.txt")}
	service, clock := newTestService(t, runner)
	if _, err := service.Sample(t.Context(), "s1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(minSpacing / 2)
	if _, err := service.Sample(t.Context(), "s1"); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("commands run = %d, want 1", got)
	}
}

func TestSampleConcurrentCallersShareOneCommand(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	output := readSample(t, "linux_ubuntu_1.txt")
	runner := &fakeRunner{reply: func(ctx context.Context, _ int32) (base.ExecResult, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return base.ExecResult{}, ctx.Err()
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, _ := newTestService(t, runner)

	const callers = 6
	results := make(chan Snapshot, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			snapshot, err := service.Sample(t.Context(), "s1")
			if err != nil {
				errs <- err
				return
			}
			results <- snapshot
		})
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	count := 0
	for snapshot := range results {
		count++
		if snapshot.Status != StatusOK || snapshot.Memory == nil {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	}
	if count != callers || runner.calls.Load() != 1 {
		t.Fatalf("results = %d, commands = %d", count, runner.calls.Load())
	}
}

func TestSampleCanceledCallerReturnsAndLeavesNoGoroutine(t *testing.T) {
	started := make(chan struct{})
	output := readSample(t, "linux_ubuntu_1.txt")
	runner := &fakeRunner{reply: func(ctx context.Context, call int32) (base.ExecResult, error) {
		if call == 1 {
			close(started)
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, _ := newTestService(t, runner)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := service.Sample(ctx, "s1")
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sample did not return after cancellation")
	}

	// A cancellation is not a host failure: the next caller samples at once instead of waiting out a backoff.
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusOK || snapshot.RetryAfterMs != 0 {
		t.Fatalf("after cancel = %+v, %v", snapshot, err)
	}
	goleak.VerifyNone(t, goleak.IgnoreCurrent())
}

func TestSampleWaiterCancellationDoesNotStopTheRunningSample(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	output := readSample(t, "linux_ubuntu_1.txt")
	runner := &fakeRunner{reply: func(context.Context, int32) (base.ExecResult, error) {
		close(started)
		<-release
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, _ := newTestService(t, runner)

	leader := make(chan error, 1)
	go func() {
		_, err := service.Sample(t.Context(), "s1")
		leader <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Sample(ctx, "s1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
}

func TestSampleBacksOffAfterFailures(t *testing.T) {
	output := readSample(t, "linux_ubuntu_1.txt")
	var fail atomic.Bool
	fail.Store(true)
	runner := &fakeRunner{reply: func(context.Context, int32) (base.ExecResult, error) {
		if fail.Load() {
			return base.ExecResult{}, errors.New("channel open failed")
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, clock := newTestService(t, runner)

	first, err := service.Sample(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusError || first.Reason == "" || first.RetryAfterMs != backoff(1).Milliseconds() {
		t.Fatalf("first failure = %+v", first)
	}
	// Inside the backoff the command does not run again.
	clock.Advance(DefaultInterval)
	waiting, err := service.Sample(t.Context(), "s1")
	if err != nil || waiting.Status != StatusError || waiting.RetryAfterMs <= 0 || runner.calls.Load() != 1 {
		t.Fatalf("inside backoff = %+v, %v, calls %d", waiting, err, runner.calls.Load())
	}
	clock.Advance(backoff(1))
	second, _ := service.Sample(t.Context(), "s1")
	if second.RetryAfterMs != backoff(2).Milliseconds() || runner.calls.Load() != 2 {
		t.Fatalf("second failure = %+v, calls %d", second, runner.calls.Load())
	}
	if backoff(2) <= backoff(1) || backoff(20) != maxBackoff {
		t.Fatalf("backoff does not grow to its cap: %v %v %v", backoff(1), backoff(2), backoff(20))
	}

	fail.Store(false)
	clock.Advance(backoff(2))
	recovered, err := service.Sample(t.Context(), "s1")
	if err != nil || recovered.Status != StatusOK || recovered.RetryAfterMs != 0 {
		t.Fatalf("recovered = %+v, %v", recovered, err)
	}
}

func TestSampleTimeoutCountsAsFailure(t *testing.T) {
	runner := &fakeRunner{reply: func(context.Context, int32) (base.ExecResult, error) {
		return base.ExecResult{}, context.DeadlineExceeded
	}}
	service, _ := newTestService(t, runner)
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusError || snapshot.RetryAfterMs == 0 {
		t.Fatalf("timeout = %+v, %v", snapshot, err)
	}
}

func TestSampleReportsUnsupportedHostsOnce(t *testing.T) {
	for _, file := range []string{"darwin.txt", "windows_cmd.txt"} {
		t.Run(file, func(t *testing.T) {
			runner := &fakeRunner{reply: sampleReplies(t, file)}
			service, clock := newTestService(t, runner)
			for range 3 {
				snapshot, err := service.Sample(t.Context(), "s1")
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Status != StatusUnsupported || snapshot.Reason == "" || snapshot.IntervalMs != 0 {
					t.Fatalf("snapshot = %+v", snapshot)
				}
				clock.Advance(time.Minute)
			}
			if runner.calls.Load() != 1 {
				t.Fatalf("an unsupported host was sampled %d times", runner.calls.Load())
			}
			// A new session on the same id (after a reconnect) is probed again.
			service.Forget("s1")
			if _, err := service.Sample(t.Context(), "s1"); err != nil || runner.calls.Load() != 2 {
				t.Fatalf("after Forget: calls %d, %v", runner.calls.Load(), err)
			}
		})
	}
}

func TestSampleKnownUnsupportedTargetRunsNothing(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "linux_ubuntu_1.txt")}
	service := NewService(func(context.Context, string) (Target, error) {
		return Target{Runner: runner, Unsupported: "PowerShell"}, nil
	})
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusUnsupported || snapshot.Reason != "PowerShell" || runner.calls.Load() != 0 {
		t.Fatalf("snapshot = %+v, %v, calls %d", snapshot, err, runner.calls.Load())
	}
}

func TestSampleRejectsBeforeRunning(t *testing.T) {
	runner := &fakeRunner{reply: sampleReplies(t, "linux_ubuntu_1.txt")}
	service, _ := newTestService(t, runner)
	if _, err := service.Sample(t.Context(), "foreign"); codeOf(err) != ipc.CodeForbidden {
		t.Fatalf("foreign error = %v", err)
	}
	if _, err := service.Sample(t.Context(), " "); codeOf(err) != ipc.CodeBadParam {
		t.Fatalf("blank id error = %v", err)
	}
	if runner.calls.Load() != 0 {
		t.Fatalf("rejected requests ran %d commands", runner.calls.Load())
	}
}

func codeOf(err error) ipc.Code {
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) {
		return ipcErr.Code
	}
	return ""
}

func TestSampleStopsReadingDisksAfterDFTimedOut(t *testing.T) {
	timedOut := readSample(t, "linux_busybox_old_kernel.txt")
	timedOut = strings.Replace(timedOut, "@@nexterm-monitor end", "@@nexterm-monitor df-timeout\n@@nexterm-monitor end", 1)
	runner := &fakeRunner{reply: func(context.Context, int32) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{Stdout: timedOut, ExitCode: &code}, nil
	}}
	service, clock := newTestService(t, runner)
	first, err := service.Sample(t.Context(), "s1")
	if err != nil || first.Status != StatusOK || !first.DisksUnavailable || len(first.Disks) != 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	for range 3 {
		clock.Advance(DefaultInterval)
		snapshot, err := service.Sample(t.Context(), "s1")
		if err != nil || !snapshot.DisksUnavailable || snapshot.Memory == nil {
			t.Fatalf("later = %+v, %v", snapshot, err)
		}
	}
	if runner.withDisks.Load() != 1 || runner.calls.Load() != 4 {
		t.Fatalf("df ran in %d of %d samples, want only the first", runner.withDisks.Load(), runner.calls.Load())
	}
	// A new session (after a reconnect) tries the disks again.
	service.Forget("s1")
	if _, err := service.Sample(t.Context(), "s1"); err != nil || runner.withDisks.Load() != 2 {
		t.Fatalf("after Forget: df runs %d, %v", runner.withDisks.Load(), err)
	}
}

func TestSampleTimeoutDropsDisksForTheSession(t *testing.T) {
	output := readSample(t, "linux_ubuntu_1.txt")
	runner := &fakeRunner{reply: func(_ context.Context, call int32) (base.ExecResult, error) {
		if call == 1 {
			return base.ExecResult{}, context.DeadlineExceeded
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, clock := newTestService(t, runner)
	if snapshot, _ := service.Sample(t.Context(), "s1"); snapshot.Status != StatusError {
		t.Fatalf("first = %+v", snapshot)
	}
	clock.Advance(backoff(1))
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.Status != StatusOK || !snapshot.DisksUnavailable {
		t.Fatalf("after timeout = %+v, %v", snapshot, err)
	}
	if runner.withDisks.Load() != 1 {
		t.Fatalf("df ran %d times after a timed-out sample", runner.withDisks.Load())
	}
}

func TestSampleOtherFailuresKeepDisks(t *testing.T) {
	output := readSample(t, "linux_ubuntu_1.txt")
	runner := &fakeRunner{reply: func(_ context.Context, call int32) (base.ExecResult, error) {
		if call == 1 {
			return base.ExecResult{}, errors.New("channel open failed")
		}
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	}}
	service, clock := newTestService(t, runner)
	_, _ = service.Sample(t.Context(), "s1")
	clock.Advance(backoff(1))
	snapshot, err := service.Sample(t.Context(), "s1")
	if err != nil || snapshot.DisksUnavailable || len(snapshot.Disks) == 0 || runner.withDisks.Load() != 2 {
		t.Fatalf("snapshot = %+v, %v, df runs %d", snapshot, err, runner.withDisks.Load())
	}
}
