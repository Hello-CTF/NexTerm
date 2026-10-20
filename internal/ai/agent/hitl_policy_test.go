package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

type hitlTestTimer struct {
	mu      sync.Mutex
	due     time.Time
	fn      func()
	stopped bool
	fired   bool
}

func (t *hitlTestTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (t *hitlTestTimer) active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.stopped && !t.fired
}

func (t *hitlTestTimer) fire() {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	fn := t.fn
	t.mu.Unlock()
	fn()
}

type hitlTestClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*hitlTestTimer
}

func newHITLTestClock() *hitlTestClock {
	return &hitlTestClock{now: time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *hitlTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *hitlTestClock) AfterFunc(delay time.Duration, fn func()) hitl.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &hitlTestTimer{due: c.now.Add(delay), fn: fn}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *hitlTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	now := c.now
	timers := append([]*hitlTestTimer(nil), c.timers...)
	c.mu.Unlock()
	for _, timer := range timers {
		if !timer.due.After(now) {
			timer.fire()
		}
	}
}

func (c *hitlTestClock) activeTimers() int {
	c.mu.Lock()
	timers := append([]*hitlTestTimer(nil), c.timers...)
	c.mu.Unlock()
	active := 0
	for _, timer := range timers {
		if timer.active() {
			active++
		}
	}
	return active
}

func hitlPolicyConfig(storage *store.Store, ttl time.Duration) Config {
	return Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return dockerConfirmChat(), 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}),
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITLTTL:     ttl,
	}
}

func TestRunnerHITLTTLConfigDrivesExpiry(t *testing.T) {
	storage := restartStore(t)
	clock := newHITLTestClock()
	config := hitlPolicyConfig(storage, time.Hour)
	config.Now = clock.Now
	runner := NewRunner(config)
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if !snapshot.Pending[0].CreatedAt.Equal(clock.Now()) {
		t.Fatalf("request did not use the injected clock: %+v", snapshot.Pending[0])
	}
	if window := snapshot.Pending[0].ExpiresAt.Sub(snapshot.Pending[0].CreatedAt); window != time.Hour {
		t.Fatalf("configured TTL did not reach the hitl manager: expiry window = %v", window)
	}

	clock.Advance(2 * time.Hour)
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); !errors.Is(err, ErrConfirmationStale) {
		t.Fatalf("expired confirmation error = %v", err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	for _, event := range events {
		if event.Type == "error" && !strings.Contains(event.Message, "过期") {
			t.Fatalf("terminal error does not report expiry: %+v", event)
		}
	}
	final, err := runner.HITLSnapshot(response.JobID)
	if err != nil || final.Status != hitl.RunStatusExpired || final.Terminal == nil || final.Terminal.Reason != hitl.TerminalExpired {
		t.Fatalf("final snapshot = %+v err=%v", final, err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
}

func TestRunnerHITLTTLConfigSurvivesRestartRecovery(t *testing.T) {
	storage := restartStore(t)
	clock := newHITLTestClock()
	config := hitlPolicyConfig(storage, time.Hour)
	config.Now = clock.Now
	runner := NewRunner(config)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := NewRunner(config)
	t.Cleanup(func() { _ = restarted.Close() })
	clock.Advance(2 * time.Hour)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
	if !strings.Contains(row.Error, "过期") {
		t.Fatalf("recovered row = %+v", row)
	}
	snapshot, err := restarted.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal == nil || snapshot.Terminal.Reason != hitl.TerminalExpired {
		t.Fatalf("restored snapshot = %+v err=%v", snapshot, err)
	}
	if err := restarted.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err == nil {
		t.Fatal("expired confirmation accepted after restart")
	}
}
