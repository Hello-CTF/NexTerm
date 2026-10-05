package hitl

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

type testClock struct {
	nanos atomic.Int64
}

func newTestClock(start time.Time) *testClock {
	clock := &testClock{}
	clock.nanos.Store(start.UnixNano())
	return clock
}

func (c *testClock) Now() time.Time {
	return time.Unix(0, c.nanos.Load())
}

func (c *testClock) Advance(delta time.Duration) {
	c.nanos.Add(int64(delta))
}

func TestTimeoutPolicyDefaultsAreExplicit(t *testing.T) {
	manager, err := NewManager(Config{Checkpoints: newFakeCheckpoints()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if DefaultTTL != 5*time.Minute {
		t.Fatalf("default ttl constant = %v", DefaultTTL)
	}
	if manager.ttl != DefaultTTL {
		t.Fatalf("default ttl = %v", manager.ttl)
	}
	if manager.terminalRetention != DefaultTerminalRetention {
		t.Fatalf("default terminal retention = %v", manager.terminalRetention)
	}
	custom, err := NewManager(Config{Checkpoints: newFakeCheckpoints(), TTL: time.Minute, TerminalRetention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = custom.Close() })
	if custom.ttl != time.Minute || custom.terminalRetention != time.Minute {
		t.Fatalf("custom policy = %v / %v", custom.ttl, custom.terminalRetention)
	}
}

func TestSecondaryRequestExpiryDoesNotKillRunningRun(t *testing.T) {
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	var nonce atomic.Uint64
	manager, err := NewManager(Config{Checkpoints: store, TTL: 40 * time.Millisecond, NewNonce: func() (string, error) {
		return fmt.Sprintf("nonce-%d", nonce.Add(1)), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-two"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-two", Tool: "exec_commands", Kind: KindConfirm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", first)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := manager.Snapshot("run")
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Pending) == 0 {
			if snapshot.Status != RunStatusRunning || snapshot.Terminal != nil {
				t.Fatalf("running run was killed by secondary expiry: %+v", snapshot)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("secondary request did not expire: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, _, err := manager.Resume(context.Background(), &fakeResumer{}, answerFor("answer-two", second)); !errors.Is(err, ErrRequestExpired) {
		t.Fatalf("late secondary answer error = %v", err)
	}
	snapshot, err := manager.Snapshot("run")
	if err != nil || snapshot.Status != RunStatusRunning || snapshot.Terminal != nil {
		t.Fatalf("snapshot after late answer = %+v, %v", snapshot, err)
	}
	terminal, err := manager.Finish("run", TerminalCompleted, nil)
	if err != nil || terminal.Reason != TerminalCompleted {
		t.Fatalf("finish after secondary expiry = %+v, %v", terminal, err)
	}
}

func TestLastPendingExpiryStillExpiresInterruptedRun(t *testing.T) {
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	manager, err := NewManager(Config{Checkpoints: store, TTL: 40 * time.Millisecond, NewNonce: func() (string, error) { return "nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := manager.Snapshot("run")
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Terminal != nil {
			if snapshot.Status != RunStatusExpired || snapshot.Terminal.Reason != TerminalExpired {
				t.Fatalf("terminal = %+v", snapshot.Terminal)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interrupted run did not expire: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func nonceCount(manager *Manager) int {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return len(manager.nonces)
}

func TestNoncesReleasedAcrossRequestLifecycle(t *testing.T) {
	h := newHarness(t)
	first := h.interrupt(t, "target-one", "call-one", `{}`)
	if nonceCount(h.manager) != 1 {
		t.Fatalf("nonces after interrupt = %d", nonceCount(h.manager))
	}
	h.store.put("checkpoint", []byte("checkpoint-two"))
	second := h.interrupt(t, "target-two", "call-two", `{}`)
	if nonceCount(h.manager) != 1 {
		t.Fatalf("nonces after stale replacement = %d", nonceCount(h.manager))
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer", second)); err != nil {
		t.Fatal(err)
	}
	if nonceCount(h.manager) != 0 {
		t.Fatalf("nonces after consume = %d", nonceCount(h.manager))
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("late", first)); !errors.Is(err, ErrRequestStale) {
		t.Fatalf("stale answer error = %v", err)
	}
	h.interrupt(t, "target-three", "call-three", `{}`)
	if nonceCount(h.manager) != 1 {
		t.Fatalf("nonces after second interrupt = %d", nonceCount(h.manager))
	}
	if _, err := h.manager.Cancel("run"); err != nil {
		t.Fatal(err)
	}
	if nonceCount(h.manager) != 0 {
		t.Fatalf("nonces after cancel = %d", nonceCount(h.manager))
	}
}

func TestTerminalRunsSweptAfterRetention(t *testing.T) {
	clock := newTestClock(time.Unix(1000, 0))
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	manager, err := NewManager(Config{
		Checkpoints: store, TTL: time.Hour, TerminalRetention: time.Minute,
		Now: clock.Now, NewNonce: func() (string, error) { return "nonce", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "old", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Finish("old", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Snapshot("old"); err != nil {
		t.Fatalf("terminal run vanished inside retention window: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := manager.RegisterRun(context.Background(), "new", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Snapshot("old"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("swept run snapshot error = %v", err)
	}
	if _, err := manager.Snapshot("new"); err != nil {
		t.Fatalf("live run swept: %v", err)
	}
}

func TestRestoreRegistersNoncesOnlyForLivePending(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	clock := newTestClock(time.Unix(1000, 0))
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")

	clock.Advance(2 * time.Hour)
	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	if nonceCount(second) != 0 {
		t.Fatalf("expired restore registered %d nonces", nonceCount(second))
	}
	if _, _, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("late", request)); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("resume of expired restored run error = %v", err)
	}
}

func TestRestoreRearmsTimerForLivePending(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: 120 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: 120 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	if nonceCount(second) != 1 {
		t.Fatalf("live restore registered %d nonces", nonceCount(second))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := second.Snapshot("run")
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Terminal != nil {
			if snapshot.Status != RunStatusExpired || snapshot.Terminal.Reason != TerminalExpired {
				t.Fatalf("restored terminal = %+v", snapshot.Terminal)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restored timer did not expire the run: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if nonceCount(second) != 0 {
		t.Fatalf("nonces after restored expiry = %d", nonceCount(second))
	}
}

func TestRestoreRunningRunWithPendingBecomesInterrupted(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	firstRequest, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRequest, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-two"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-two", Tool: "exec_commands", Kind: KindConfirm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", firstRequest)); err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Snapshot("run")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != RunStatusInterrupted || snapshot.Terminal != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].ID != secondRequest.ID {
		t.Fatalf("restored running-with-pending snapshot = %+v", snapshot)
	}
	if _, _, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-two", secondRequest)); err != nil {
		t.Fatalf("resume of restored pending failed: %v", err)
	}
}

func TestRestoreRunningRunWithoutPendingFailsHonestly(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Snapshot("run")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Terminal == nil || snapshot.Terminal.Reason != TerminalFailed || snapshot.Status != RunStatusFailed {
		t.Fatalf("restored zombie snapshot = %+v", snapshot)
	}
	if _, err := second.Finish("run", TerminalCompleted, nil); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("late finish error = %v", err)
	}
}

func TestExpiryRacingAnswersAndCancelStaysConsistent(t *testing.T) {
	for iteration := range 30 {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			store := newFakeCheckpoints()
			store.put("checkpoint", []byte("checkpoint-one"))
			manager, err := NewManager(Config{Checkpoints: store, TTL: 10 * time.Millisecond, NewNonce: func() (string, error) { return "nonce", nil }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close() })
			if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
				t.Fatal(err)
			}
			request, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
				RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
			})
			if err != nil {
				t.Fatal(err)
			}
			resumer := &fakeResumer{}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, _, _ = manager.Resume(context.Background(), resumer, answerFor("answer", request))
				}()
			}
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, _ = manager.Cancel("run")
				}()
			}
			close(start)
			wg.Wait()
			if resumer.count() > 1 {
				t.Fatalf("resume calls = %d", resumer.count())
			}
			snapshot, err := manager.Snapshot("run")
			if err != nil {
				t.Fatal(err)
			}
			events, err := manager.Events("run", 0)
			if err != nil {
				t.Fatal(err)
			}
			terminals := 0
			for _, event := range events {
				if event.Kind == EventTerminal {
					terminals++
				}
			}
			if terminals > 1 {
				t.Fatalf("terminal events = %d, events = %+v", terminals, events)
			}
			if snapshot.Terminal != nil && terminals != 1 {
				t.Fatalf("snapshot terminal without event: %+v", snapshot)
			}
		})
	}
}
