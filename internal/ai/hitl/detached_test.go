package hitl

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

// gatedFailStore fails exactly one SaveRun after a test-gated interlude, so
// an entry point can be started and parked while the failing save is in
// flight. It counts every SaveRun call.
type gatedFailStore struct {
	*fakeRunStore
	mu         sync.Mutex
	fail       bool
	saves      int
	persisting chan struct{}
	allow      chan struct{}
}

func (s *gatedFailStore) SaveRun(ctx context.Context, blob RunBlob) error {
	s.mu.Lock()
	s.saves++
	fail := s.fail
	if fail {
		s.fail = false
	}
	s.mu.Unlock()
	if fail {
		close(s.persisting)
		<-s.allow
		return errors.New("injected save failure")
	}
	return s.fakeRunStore.SaveRun(ctx, blob)
}

func (s *gatedFailStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

// detachedRace drives the deterministic restore-rollback interleaving: a
// pending run is restored with its SaveRun gated, and an entry point is
// started with its fetch ordered strictly before the rollback's delete by
// holding the manager read lock — readers run concurrently with it, while
// the delete waits for the release. No scheduling luck is involved: the
// entry's fetch can only succeed before the delete, never after it.
type detachedRace struct {
	manager     *Manager
	store       *gatedFailStore
	checkpoints *fakeCheckpoints
	clock       *fakeClock
	offset      *atomic.Int64
	blob        RunBlob
	request     Interrupt
	allow       chan struct{}
	restoreErr  chan error
	baseline    int
}

func newDetachedRace(t *testing.T) *detachedRace {
	t.Helper()
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	clock := newFakeClock()
	offset := &atomic.Int64{}
	now := func() time.Time { return clock.Now().Add(time.Duration(offset.Load())) }
	store := &gatedFailStore{fakeRunStore: newFakeRunStore(), persisting: make(chan struct{}), allow: make(chan struct{})}
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: store, TTL: time.Hour, Now: now, AfterFunc: clock.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	blob := store.snapshot("run")
	if len(blob.Data) == 0 {
		t.Fatal("interrupt was not persisted")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: store, TTL: time.Hour, Now: now, AfterFunc: clock.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	race := &detachedRace{
		manager: second, store: store, checkpoints: checkpoints, clock: clock, offset: offset,
		blob: blob, request: request, allow: store.allow,
		restoreErr: make(chan error, 1),
		baseline:   store.saveCount(),
	}
	go func() { race.restoreErr <- second.Restore(blob) }()
	<-store.persisting
	return race
}

// enter starts entry in a goroutine with its fetch explicitly proven first:
// the wrapper takes the run through the manager's own lookup and signals,
// then the rollback is released while the test holds the manager read lock,
// so the entry's own fetch strictly precedes the delete. It returns the
// entry's error after the callback completes.
func (r *detachedRace) enter(t *testing.T, entry func() error) error {
	t.Helper()
	r.manager.mu.RLock()
	fetched := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		if _, err := r.manager.run("run"); err != nil {
			done <- err
			return
		}
		close(fetched)
		done <- entry()
	}()
	<-fetched
	close(r.allow)
	r.manager.mu.RUnlock()
	return <-done
}

// assertUntouched asserts the rollback left no trace: exactly one extra
// SaveRun (the failed restore persist), unchanged blob bytes, no checkpoint
// delete, and no ghost registration.
func (r *detachedRace) assertUntouched(t *testing.T) {
	t.Helper()
	if saves := r.store.saveCount(); saves != r.baseline+1 {
		t.Fatalf("SaveRun calls = %d, want only the failed restore persist (%d)", saves, r.baseline+1)
	}
	if again := r.store.snapshot("run"); !bytes.Equal(again.Data, r.blob.Data) {
		t.Fatal("blob rewritten after rolled-back restore")
	}
	r.checkpoints.mu.Lock()
	deletes := r.checkpoints.deletes["checkpoint"]
	r.checkpoints.mu.Unlock()
	if deletes != 0 {
		t.Fatalf("checkpoint deleted by a detached entry: %d", deletes)
	}
	if _, err := r.manager.run("run"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ghost run registered: %v", err)
	}
}

// resumeAfterRetry proves the untouched blob still restores and resumes.
func (r *detachedRace) resumeAfterRetry(t *testing.T) {
	t.Helper()
	r.offset.Store(0)
	if err := r.manager.Restore(r.blob); err != nil {
		t.Fatalf("retry restore: %v", err)
	}
	resume, iterator, err := r.manager.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", r.request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after retry = %+v err=%v", resume, err)
	}
	if _, err := r.manager.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
}

// TestStoreRestoreRollbackDetachesStartedExpireCallback covers the timer
// interleaving the plain rollback test cannot reach: the expire callback has
// already taken the run pointer when the restore's SaveRun fails. Timer.Stop
// cannot recall a started callback, so after the rollback unregisters the
// run the callback must fail closed — no blob rewrite, no checkpoint delete
// — and the untouched blob must still restore and resume.
func TestStoreRestoreRollbackDetachesStartedExpireCallback(t *testing.T) {
	race := newDetachedRace(t)
	// Arm the expiry so the entered callback classifies the request as expired.
	race.offset.Store(int64(2 * time.Hour))
	if err := race.enter(t, func() error {
		race.manager.expire("run", race.request.ID)
		return nil
	}); err != nil {
		t.Fatalf("expire callback: %v", err)
	}
	if err := <-race.restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	race.assertUntouched(t)
	race.resumeAfterRetry(t)
}

// TestStoreRestoreRollbackDetachesStartedFinish covers Finish racing the
// rollback: the finish call has taken the run pointer and waits on run.mu
// when the rollback unregisters the run; it must fail closed instead of
// terminalizing the detached run and deleting its checkpoint.
func TestStoreRestoreRollbackDetachesStartedFinish(t *testing.T) {
	race := newDetachedRace(t)
	err := race.enter(t, func() error {
		_, err := race.manager.Finish("run", TerminalCompleted, nil)
		return err
	})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("detached finish error = %v", err)
	}
	if err := <-race.restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	race.assertUntouched(t)
	race.resumeAfterRetry(t)
}

// TestStoreRestoreRollbackDetachesStartedCancel covers Cancel racing the
// rollback with the same fail-closed contract as Finish.
func TestStoreRestoreRollbackDetachesStartedCancel(t *testing.T) {
	race := newDetachedRace(t)
	err := race.enter(t, func() error {
		_, err := race.manager.Cancel("run")
		return err
	})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("detached cancel error = %v", err)
	}
	if err := <-race.restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	race.assertUntouched(t)
	race.resumeAfterRetry(t)
}

// TestStoreRestoreRollbackDetachesStartedResume covers Resume racing the
// rollback: the resume has taken the run pointer and waits on run.mu when
// the rollback unregisters the run; it must fail closed instead of
// persisting state and starting the resumer.
func TestStoreRestoreRollbackDetachesStartedResume(t *testing.T) {
	race := newDetachedRace(t)
	resumer := &fakeResumer{}
	err := race.enter(t, func() error {
		_, _, err := race.manager.Resume(context.Background(), resumer, answerFor("answer-one", race.request))
		return err
	})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("detached resume error = %v", err)
	}
	resumer.mu.Lock()
	resumeCalls := len(resumer.calls)
	resumer.mu.Unlock()
	if resumeCalls != 0 {
		t.Fatalf("detached resume started the resumer %d times", resumeCalls)
	}
	if err := <-race.restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	race.assertUntouched(t)
	race.resumeAfterRetry(t)
}

// TestStoreRestoreRollbackDetachesStartedInterrupt covers Interrupt racing
// the rollback: the interrupt has taken the run pointer and waits on run.mu
// when the rollback unregisters the run; it must fail closed instead of
// recording a request and persisting the detached run.
func TestStoreRestoreRollbackDetachesStartedInterrupt(t *testing.T) {
	race := newDetachedRace(t)
	err := race.enter(t, func() error {
		_, err := race.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-two", IsRootCause: true}, InterruptInput{
			RunID: "run", CheckpointID: "checkpoint", CallID: "call-two", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
		})
		return err
	})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("detached interrupt error = %v", err)
	}
	if err := <-race.restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	race.assertUntouched(t)
	race.resumeAfterRetry(t)
}

// TestStoreRestoreRollbackCloseStaysHarmless covers Close racing the
// rollback: the manager's closed flag is set in the same critical section
// as the run snapshot, so observing it proves the snapshot caught the run
// before the delete. Close then parks the detached run without finishing
// it: no blob rewrite, no checkpoint delete, and the untouched blob still
// restores and resumes.
func TestStoreRestoreRollbackCloseStaysHarmless(t *testing.T) {
	race := newDetachedRace(t)
	closeErr := make(chan error, 1)
	go func() { closeErr <- race.manager.Close() }()
	waitForManagerClosed(t, race.manager)
	close(race.allow)
	if err := <-closeErr; err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := <-race.restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	race.assertUntouched(t)
	// Close shut the manager down; the retry runs on a fresh manager over
	// the same store.
	third, err := NewManager(Config{Checkpoints: race.checkpoints, Store: race.store, TTL: time.Hour, Now: func() time.Time { return race.clock.Now().Add(time.Duration(race.offset.Load())) }, AfterFunc: race.clock.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if err := third.Restore(race.blob); err != nil {
		t.Fatalf("retry restore: %v", err)
	}
	resume, iterator, err := third.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", race.request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after retry = %+v err=%v", resume, err)
	}
	if _, err := third.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
}

// waitForManagerClosed polls the closed flag: it is set in the same
// critical section as Close's run snapshot, so observing it proves the
// snapshot completed.
func waitForManagerClosed(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.RLock()
		closed := m.closed
		m.mu.RUnlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("manager did not close")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
