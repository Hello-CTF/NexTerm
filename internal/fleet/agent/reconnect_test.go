package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestJitterDelayStaysWithinTwentyPercent(t *testing.T) {
	base := 10 * time.Second
	for unit, want := range map[float64]time.Duration{0: 8 * time.Second, 0.5: 10 * time.Second, 0.999999: 12*time.Second - time.Microsecond*10} {
		got := jitterDelay(base, unit)
		if diff := got - want; diff < -time.Millisecond || diff > time.Millisecond {
			t.Errorf("jitterDelay(%v, %v) = %v, want about %v", base, unit, got, want)
		}
	}
	runtime := &Runtime{jitter: func() float64 { return 0 }}
	if got := runtime.jittered(5 * time.Second); got != 4*time.Second {
		t.Fatalf("injected jitter source ignored: %v", got)
	}
	runtime.jitter = nil
	for i := 0; i < 100; i++ {
		if got := runtime.jittered(time.Second); got < 800*time.Millisecond || got >= 1200*time.Millisecond {
			t.Fatalf("default jitter %v out of range", got)
		}
	}
}

type countingManager struct {
	fakeManager
	statusCalls atomic.Int32
}

func (m *countingManager) Status(ctx context.Context) ServiceState {
	m.statusCalls.Add(1)
	return m.fakeManager.Status(ctx)
}

func TestReconcileServiceReusesSettledStatus(t *testing.T) {
	manager := &countingManager{}
	agentRuntime := newTestRuntime(t, t.TempDir(), []BaseURLEntry{{URL: "http://127.0.0.1:1"}}, manager)
	agentRuntime.desired.DesiredAutostart = true
	ctx := context.Background()

	agentRuntime.reconcileService(ctx) // installs, then re-reads the status
	if installs, _ := manager.counts(); installs != 1 {
		t.Fatalf("installs = %d, want 1", installs)
	}
	settled := manager.statusCalls.Load()
	for i := 0; i < serviceRecheckCycles-1; i++ {
		agentRuntime.reconcileService(ctx)
	}
	if got := manager.statusCalls.Load(); got != settled {
		t.Fatalf("service manager asked %d more times while the status was settled", got-settled)
	}
	agentRuntime.reconcileService(ctx)
	if got := manager.statusCalls.Load(); got != settled+1 {
		t.Fatalf("status re-read count = %d, want %d after the recheck interval", got, settled+1)
	}

	// A desired-state flip is acted on immediately, not after the recheck interval.
	agentRuntime.mu.Lock()
	agentRuntime.desired.DesiredAutostart = false
	agentRuntime.mu.Unlock()
	agentRuntime.reconcileService(ctx)
	if _, uninstalls := manager.counts(); uninstalls != 1 {
		t.Fatalf("uninstalls = %d, want 1 right after the desired state flipped", uninstalls)
	}
	if state := agentRuntime.serviceState; state.LastReconcileAt == 0 {
		t.Fatal("service state not refreshed")
	}
}

func TestRetryWaitNeverShortensAServerGivenRetryAfter(t *testing.T) {
	const delay = 10 * time.Second
	for _, unit := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		if got := retryWait(delay, true, jitterDelay(delay, unit)); got < delay {
			t.Fatalf("unit %v: wait %v is shorter than the server's Retry-After %v", unit, got, delay)
		}
	}
	// Our own backoff keeps the symmetric jitter.
	if got := retryWait(delay, false, jitterDelay(delay, 0)); got >= delay {
		t.Fatalf("own backoff should be allowed below the base delay, got %v", got)
	}
}
