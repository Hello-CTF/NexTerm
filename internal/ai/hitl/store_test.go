package hitl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: 40 * time.Millisecond})
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

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	time.Sleep(60 * time.Millisecond)
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
