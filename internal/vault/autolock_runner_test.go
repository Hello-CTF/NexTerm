package vault

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const autoLockTestTimeout = 5 * time.Second

type fakeAutoLockTicker struct {
	ticks     chan time.Time
	intervals chan time.Duration
	stopped   chan struct{}
	stopOnce  sync.Once
	stops     atomic.Int32
}

func newFakeAutoLockTicker() *fakeAutoLockTicker {
	return &fakeAutoLockTicker{
		ticks:     make(chan time.Time),
		intervals: make(chan time.Duration, 1),
		stopped:   make(chan struct{}),
	}
}

func (t *fakeAutoLockTicker) factory(interval time.Duration) (<-chan time.Time, func()) {
	t.intervals <- interval
	return t.ticks, t.stop
}

func (t *fakeAutoLockTicker) stop() {
	t.stops.Add(1)
	t.stopOnce.Do(func() { close(t.stopped) })
}

type autoLockTestClock struct {
	now      atomic.Int64
	observed chan struct{}
	report   atomic.Bool
}

func (c *autoLockTestClock) Now() int64 {
	if c.report.Load() {
		c.observed <- struct{}{}
	}
	return c.now.Load()
}

func newAutoLockRunnerTestVault(t *testing.T, factory autoLockTickerFactory) (*Vault, *autoLockTestClock) {
	t.Helper()
	clock := &autoLockTestClock{observed: make(chan struct{}, 16)}
	clock.now.Store(1000)
	v := loadTestVault(testStore(t), &fakeProtector{}, func(v *Vault) {
		v.now = clock.Now
		v.newAutoLockTicker = factory
	})
	ctx := context.Background()
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.SetAutoLock(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clock.now.Add(60_001)
	return v, clock
}

func startAutoLockRunner(t *testing.T, v *Vault, ticker *fakeAutoLockTicker) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		v.RunAutoLock(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		waitAutoLockEvent(t, done, "RunAutoLock did not stop after context cancellation")
	})
	select {
	case interval := <-ticker.intervals:
		if interval != time.Minute {
			t.Fatalf("auto-lock ticker interval = %v, want %v", interval, time.Minute)
		}
	case <-time.After(autoLockTestTimeout):
		t.Fatal("RunAutoLock did not create its ticker")
	}
	return cancel, done
}

func sendAutoLockTick(t *testing.T, ticker *fakeAutoLockTicker) {
	t.Helper()
	select {
	case ticker.ticks <- time.Time{}:
	case <-time.After(autoLockTestTimeout):
		t.Fatal("RunAutoLock did not receive an injected tick")
	}
}

func waitAutoLockEvent(t *testing.T, event <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(autoLockTestTimeout):
		t.Fatal(message)
	}
}

func TestRunAutoLockLocksOnInjectedTicksAndStops(t *testing.T) {
	ticker := newFakeAutoLockTicker()
	v, clock := newAutoLockRunnerTestVault(t, ticker.factory)
	if !v.Status().Unlocked {
		t.Fatal("precondition: vault must start unlocked")
	}
	cancel, done := startAutoLockRunner(t, v, ticker)
	clock.report.Store(true)

	sendAutoLockTick(t, ticker)
	waitAutoLockEvent(t, clock.observed, "RunAutoLock did not check the idle deadline")
	if v.Status().Unlocked {
		t.Fatal("vault did not lock after the injected tick")
	}

	clock.report.Store(false)
	if err := v.UnlockMaster(context.Background(), "correct-password"); err != nil {
		t.Fatal(err)
	}
	clock.now.Add(60_001)
	clock.report.Store(true)
	sendAutoLockTick(t, ticker)
	waitAutoLockEvent(t, clock.observed, "RunAutoLock did not check a repeated tick")
	if v.Status().Unlocked {
		t.Fatal("vault did not lock again after a repeated tick")
	}

	cancel()
	cancel()
	waitAutoLockEvent(t, done, "RunAutoLock did not return after context cancellation")
	waitAutoLockEvent(t, ticker.stopped, "RunAutoLock did not stop its ticker")
	if stops := ticker.stops.Load(); stops != 1 {
		t.Fatalf("ticker Stop calls = %d, want 1", stops)
	}
	select {
	case ticker.ticks <- time.Time{}:
		t.Fatal("RunAutoLock accepted a tick after returning")
	default:
	}
}

func TestRunAutoLockConcurrentRunners(t *testing.T) {
	const runners = 4
	created := make(chan *fakeAutoLockTicker, runners)
	factory := func(interval time.Duration) (<-chan time.Time, func()) {
		ticker := newFakeAutoLockTicker()
		ticks, stop := ticker.factory(interval)
		created <- ticker
		return ticks, stop
	}
	v, clock := newAutoLockRunnerTestVault(t, factory)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runnersDone sync.WaitGroup
	runnersDone.Add(runners)
	for range runners {
		go func() {
			defer runnersDone.Done()
			v.RunAutoLock(ctx)
		}()
	}
	go func() {
		runnersDone.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitAutoLockEvent(t, done, "concurrent RunAutoLock calls did not stop")
	})
	clock.report.Store(true)

	tickers := make([]*fakeAutoLockTicker, 0, runners)
	for range runners {
		select {
		case ticker := <-created:
			tickers = append(tickers, ticker)
		case <-time.After(autoLockTestTimeout):
			t.Fatal("concurrent RunAutoLock did not create its ticker")
		}
	}
	for _, ticker := range tickers {
		select {
		case interval := <-ticker.intervals:
			if interval != time.Minute {
				t.Fatalf("auto-lock ticker interval = %v, want %v", interval, time.Minute)
			}
		case <-time.After(autoLockTestTimeout):
			t.Fatal("concurrent RunAutoLock did not report its ticker interval")
		}
		sendAutoLockTick(t, ticker)
	}
	for range runners {
		waitAutoLockEvent(t, clock.observed, "concurrent RunAutoLock did not check the idle deadline")
	}
	if v.Status().Unlocked {
		t.Fatal("vault remained unlocked after concurrent auto-lock runners")
	}

	cancel()
	waitAutoLockEvent(t, done, "concurrent RunAutoLock calls did not return")
	for _, ticker := range tickers {
		waitAutoLockEvent(t, ticker.stopped, "concurrent RunAutoLock did not stop its ticker")
		if stops := ticker.stops.Load(); stops != 1 {
			t.Fatalf("ticker Stop calls = %d, want 1", stops)
		}
	}
}
