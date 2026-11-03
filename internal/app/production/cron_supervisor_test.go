package production

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCronRuntimeRestartsSchedulerWithBackoff(t *testing.T) {
	storageErr := errors.New("injected storage failure")
	runs := make(chan int, 8)
	waits := make(chan time.Duration, 8)
	calls := 0
	runtime := &cronRuntime{
		run: func(ctx context.Context) error {
			calls++
			runs <- calls
			if calls < 3 {
				return storageErr
			}
			<-ctx.Done()
			return ctx.Err()
		},
		wait: func(ctx context.Context, d time.Duration) error {
			waits <- d
			return ctx.Err()
		},
		now: func() time.Time { return time.Unix(0, 0) },
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	recv := func(ch <-chan time.Duration) time.Duration {
		select {
		case d := <-ch:
			return d
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for backoff request")
			return 0
		}
	}
	if d := recv(waits); d != time.Second {
		t.Fatalf("first backoff = %s", d)
	}
	if d := recv(waits); d != 2*time.Second {
		t.Fatalf("second backoff = %s", d)
	}
	for i := 1; i <= 3; i++ {
		select {
		case <-runs:
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d never started", i)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-runs:
		t.Fatalf("run %d started after shutdown", n)
	default:
	}
}

func TestCronRuntimeShutdownInterruptsBackoffWait(t *testing.T) {
	waiting := make(chan struct{})
	runs := make(chan struct{}, 4)
	runtime := &cronRuntime{
		run: func(context.Context) error {
			runs <- struct{}{}
			return errors.New("fail")
		},
		wait: func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		},
		now: func() time.Time { return time.Unix(0, 0) },
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("never entered backoff")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown during backoff: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("scheduler restarted after shutdown: %d runs", len(runs))
	}
}

func TestCronRuntimeHealthyRunResetsBackoff(t *testing.T) {
	waits := make(chan time.Duration, 8)
	var clock time.Time
	calls := 0
	runtime := &cronRuntime{
		run: func(ctx context.Context) error {
			calls++
			if calls == 2 {
				clock = clock.Add(cronRestartBackoffMax)
			}
			if calls >= 3 {
				<-ctx.Done()
				return ctx.Err()
			}
			return errors.New("fail")
		},
		wait: func(ctx context.Context, d time.Duration) error {
			waits <- d
			return ctx.Err()
		},
		now: func() time.Time { return clock },
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runtime.supervise(ctx)
	}()
	for i := 0; i < 2; i++ {
		select {
		case d := <-waits:
			if d != time.Second {
				t.Fatalf("backoff %d = %s, want 1s (healthy run resets)", i, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for backoff")
		}
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}
