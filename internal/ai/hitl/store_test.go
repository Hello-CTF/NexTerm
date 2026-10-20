package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

