package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/adk"
)

type fakeRunStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{blobs: make(map[string][]byte)}
}

func (s *fakeRunStore) SaveRun(_ context.Context, blob RunBlob) error {
	s.mu.Lock()
	s.blobs[blob.ID] = append([]byte(nil), blob.Data...)
	s.mu.Unlock()
	return nil
}

func (s *fakeRunStore) ListRuns(_ context.Context) ([]RunBlob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blobs := make([]RunBlob, 0, len(s.blobs))
	for id, data := range s.blobs {
		blobs = append(blobs, RunBlob{ID: id, Data: append([]byte(nil), data...)})
	}
	return blobs, nil
}

func (s *fakeRunStore) snapshot(id string) RunBlob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RunBlob{ID: id, Data: append([]byte(nil), s.blobs[id]...)}
}

func TestStoreRestartRestoresPendingInterrupt(t *testing.T) {
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
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")
	if len(captured.Data) == 0 {
		t.Fatal("interrupt was not persisted")
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	if err := second.Restore(captured); !errors.Is(err, ErrRunExists) {
		t.Fatalf("duplicate restore error = %v", err)
	}
	snapshot, err := second.Snapshot("run")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != RunStatusInterrupted || snapshot.Attempt != 1 || len(snapshot.Pending) != 1 {
		t.Fatalf("restored snapshot = %+v", snapshot)
	}
	pending := snapshot.Pending[0]
	if pending.ID != request.ID || pending.Nonce != request.Nonce || pending.CallID != request.CallID {
		t.Fatalf("restored pending = %+v want %+v", pending, request)
	}
	events, err := second.Events("run", 0)
	if err != nil || len(events) != 1 || events[0].Kind != EventInterrupted || events[0].RequestID != request.ID {
		t.Fatalf("restored events = %+v err=%v", events, err)
	}

	resumer := &fakeResumer{}
	resume, iterator, err := second.Resume(context.Background(), resumer, answerFor("answer-one", request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after restart = %+v err=%v", resume, err)
	}
	terminal, err := second.Finish("run", TerminalCompleted, nil)
	if err != nil || terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %+v err=%v", terminal, err)
	}
}

func TestStoreRestartMarksExpiredPendingHonestly(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	clock := newFakeClock()
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: clock.Now, AfterFunc: clock.AfterFunc})
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
	captured := blobStore.snapshot("run")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: clock.Now, AfterFunc: clock.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	clock.Advance(2 * time.Hour)
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Snapshot("run")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != RunStatusExpired || snapshot.Terminal == nil || snapshot.Terminal.Reason != TerminalExpired || len(snapshot.Pending) != 0 {
		t.Fatalf("expired restored snapshot = %+v", snapshot)
	}
	if _, _, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-late", request)); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("resume of expired restored run error = %v", err)
	}
}

func TestStoreRestartRestoresTerminalRun(t *testing.T) {
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
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request)); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Finish("run", TerminalCompleted, nil); err != nil {
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
	if snapshot.Status != RunStatusCompleted || snapshot.Terminal == nil || snapshot.Terminal.Reason != TerminalCompleted || len(snapshot.Pending) != 0 {
		t.Fatalf("restored terminal snapshot = %+v", snapshot)
	}
	events, err := second.Events("run", 0)
	if err != nil || len(events) != 3 || events[2].Kind != EventTerminal {
		t.Fatalf("restored terminal events = %+v err=%v", events, err)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	manager, err := NewManager(Config{Checkpoints: newFakeCheckpoints(), Store: newFakeRunStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Restore(RunBlob{ID: "run", Data: []byte("not-json")}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("garbage restore error = %v", err)
	}
	if err := manager.Restore(RunBlob{ID: "other", Data: []byte(`{"id":"run","checkpointId":"cp"}`)}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("mismatched id restore error = %v", err)
	}
}

// ownerGuardedRunStore mimics the real store's owner-guarded upsert: a save
// whose context owner differs from the stored row's owner is a write conflict.
type ownerGuardedRunStore struct {
	mu     sync.Mutex
	owners map[string]string
	blobs  map[string][]byte
}

func newOwnerGuardedRunStore() *ownerGuardedRunStore {
	return &ownerGuardedRunStore{owners: make(map[string]string), blobs: make(map[string][]byte)}
}

func (s *ownerGuardedRunStore) SaveRun(ctx context.Context, blob RunBlob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := ipc.OwnerID(ctx)
	if existing, ok := s.owners[blob.ID]; ok && existing != owner {
		return errors.New("store: write conflict")
	}
	s.owners[blob.ID] = owner
	s.blobs[blob.ID] = append([]byte(nil), blob.Data...)
	return nil
}

func (s *ownerGuardedRunStore) ListRuns(context.Context) ([]RunBlob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blobs := make([]RunBlob, 0, len(s.blobs))
	for id, data := range s.blobs {
		blobs = append(blobs, RunBlob{ID: id, Data: append([]byte(nil), data...)})
	}
	return blobs, nil
}

func (s *ownerGuardedRunStore) snapshot(id string) (RunBlob, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RunBlob{ID: id, Data: append([]byte(nil), s.blobs[id]...)}, s.owners[id]
}

func TestStoreRestartRestoresOwnedRunUnderRecordedOwner(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newOwnerGuardedRunStore()
	ownerCtx := ipc.WithUserID(context.Background(), "user-1")
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(ownerCtx, "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(ownerCtx, &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, owner := blobStore.snapshot("run")
	if len(captured.Data) == 0 || owner != "user-1" {
		t.Fatalf("persisted blob owner = %q bytes = %d", owner, len(captured.Data))
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatalf("restore of owned run: %v", err)
	}
	if _, owner := blobStore.snapshot("run"); owner != "user-1" {
		t.Fatalf("restored blob owner = %q", owner)
	}
	resume, iterator, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after owned restart = %+v err=%v", resume, err)
	}
	if _, err := second.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
}

// failingSaveRunStore injects SaveRun failures while its failure budget
// lasts; the wrapped fake keeps every blob so a failed restore leaves the
// persisted state untouched for the retry.
type failingSaveRunStore struct {
	*fakeRunStore
	mu       sync.Mutex
	failures int
}

func (s *failingSaveRunStore) failNext(n int) {
	s.mu.Lock()
	s.failures = n
	s.mu.Unlock()
}

func (s *failingSaveRunStore) SaveRun(ctx context.Context, blob RunBlob) error {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return errors.New("injected save failure")
	}
	s.mu.Unlock()
	return s.fakeRunStore.SaveRun(ctx, blob)
}

// TestStoreRestartRestoresLegacyBlobUnderRowOwner covers blobs written
// before the owner field existed: the JSON carries no ownerId, but the
// persisted row's owner is authoritative. The restore must seed that owner
// so the re-persist passes the owner guard instead of conflicting as an
// owner-less rewrite of an owned row.
func TestStoreRestartRestoresLegacyBlobUnderRowOwner(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newOwnerGuardedRunStore()
	ownerCtx := ipc.WithUserID(context.Background(), "user-1")
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(ownerCtx, "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(ownerCtx, &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, owner := blobStore.snapshot("run")
	if len(captured.Data) == 0 || owner != "user-1" {
		t.Fatalf("persisted blob owner = %q bytes = %d", owner, len(captured.Data))
	}

	// The pre-owner format: identical blob without the ownerId field.
	var legacy map[string]any
	if err := json.Unmarshal(captured.Data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "ownerId")
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(RunBlob{ID: captured.ID, Data: legacyData, OwnerID: owner}); err != nil {
		t.Fatalf("restore of legacy owned blob: %v", err)
	}
	if _, owner := blobStore.snapshot("run"); owner != "user-1" {
		t.Fatalf("restored blob owner = %q", owner)
	}
	resume, iterator, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after legacy restart = %+v err=%v", resume, err)
	}
	if _, err := second.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
}

// TestStoreRestorePersistFailureRollsBackAndRetries covers a retryable
// persistence failure during restore: the in-memory registration, nonces,
// and timers roll back (no ghost run), the persisted blob stays untouched,
// and a later restore of the same blob succeeds.
func TestStoreRestorePersistFailureRollsBackAndRetries(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := &failingSaveRunStore{fakeRunStore: newFakeRunStore()}
	clock := newFakeClock()
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: clock.Now, AfterFunc: clock.AfterFunc})
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
	captured := blobStore.snapshot("run")
	if len(captured.Data) == 0 {
		t.Fatal("interrupt was not persisted")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: clock.Now, AfterFunc: clock.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	blobStore.failNext(1)
	err = second.Restore(captured)
	if !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore persist error = %v", err)
	}
	// The failed restore leaves no ghost run, nonce, or timer behind.
	if _, err := second.Snapshot("run"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ghost run after failed restore: %v", err)
	}
	second.mu.RLock()
	nonces := len(second.nonces)
	second.mu.RUnlock()
	if nonces != 0 {
		t.Fatalf("nonces registered after failed restore = %d", nonces)
	}
	if active := clock.activeTimers(); active != 0 {
		t.Fatalf("active timers after failed restore = %d", active)
	}
	// The persisted blob survives untouched for the retry.
	if again := blobStore.snapshot("run"); len(again.Data) == 0 {
		t.Fatal("blob lost after failed restore")
	}

	// The same blob restores cleanly once the store recovers.
	if err := second.Restore(captured); err != nil {
		t.Fatalf("retry restore: %v", err)
	}
	resume, iterator, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after retry = %+v err=%v", resume, err)
	}
	if _, err := second.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
}

// gatedFailStore fails exactly one SaveRun after a test-gated interlude, so
// an expire callback can be started and parked while the failing save is in
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

// TestStoreRestoreRollbackDetachesStartedExpireCallback covers the timer
// interleaving the plain rollback test cannot reach: the expire callback has
// already entered expire and fetched the run when the restore's SaveRun
// fails. Timer.Stop cannot recall a started callback, so after the rollback
// unregisters the run the callback must fail closed — no blob rewrite, no
// checkpoint delete — and the untouched blob must still restore and resume.
func TestStoreRestoreRollbackDetachesStartedExpireCallback(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	clock := newFakeClock()
	var offset atomic.Int64
	now := func() time.Time { return clock.Now().Add(time.Duration(offset.Load())) }

	blobStore := &gatedFailStore{fakeRunStore: newFakeRunStore(), persisting: make(chan struct{}), allow: make(chan struct{})}
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: now, AfterFunc: clock.AfterFunc})
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
	captured := blobStore.snapshot("run")
	if len(captured.Data) == 0 {
		t.Fatal("interrupt was not persisted")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, Now: now, AfterFunc: clock.AfterFunc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	blobStore.mu.Lock()
	blobStore.fail = true
	blobStore.mu.Unlock()
	baseline := blobStore.saveCount()

	restoreErr := make(chan error, 1)
	go func() { restoreErr <- second.Restore(captured) }()

	// Restore is now parked inside the failing SaveRun, holding run.mu.
	<-blobStore.persisting
	// Enter the expire callback while the save is in flight. Gating it on
	// the manager mutex orders the callback's fetch before the rollback's
	// delete deterministically: both then queue on the mutex, and the
	// read lock wins against the queued writer.
	second.mu.Lock()
	// Arm the expiry so the entered callback classifies the request as expired.
	offset.Store(int64(2 * time.Hour))
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		second.expire("run", request.ID)
	}()
	// Let the save fail: the rollback blocks on the manager mutex behind
	// the callback's fetch.
	close(blobStore.allow)
	second.mu.Unlock()

	// The callback fetches the run (still registered), parks on run.mu, and
	// after the rollback unregisters the run it must fail closed.
	<-callbackDone
	if err := <-restoreErr; !errors.Is(err, ErrRestorePersist) {
		t.Fatalf("restore error = %v", err)
	}
	if saves := blobStore.saveCount(); saves != baseline+1 {
		t.Fatalf("SaveRun calls = %d, want only the failed restore persist (%d)", saves, baseline+1)
	}
	if again := blobStore.snapshot("run"); !bytes.Equal(again.Data, captured.Data) {
		t.Fatal("blob rewritten after rolled-back restore")
	}
	checkpoints.mu.Lock()
	deletes := checkpoints.deletes["checkpoint"]
	checkpoints.mu.Unlock()
	if deletes != 0 {
		t.Fatalf("checkpoint deleted by the detached callback: %d", deletes)
	}
	if _, err := second.run("run"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ghost run registered: %v", err)
	}

	// The untouched blob restores cleanly and the run resumes.
	offset.Store(0)
	if err := second.Restore(captured); err != nil {
		t.Fatalf("retry restore: %v", err)
	}
	resume, iterator, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request))
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("resume after retry = %+v err=%v", resume, err)
	}
	if _, err := second.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
}
