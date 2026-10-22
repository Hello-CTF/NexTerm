package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRecoveryNoOpPreservesResumedCheckpoint(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "checkpoint-race", nil)
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryRun(t, storage, ctx, "run-x", conversation.ID, store.RunStatusInterrupted, false)
	checkpoints := NewStoreCheckpoints(storage)
	if err := checkpoints.Set(ctx, "run-x", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	gated := &gatedAtomicStore{Store: storage, entered: make(chan struct{}), release: make(chan struct{})}
	runner := NewRunner(Config{Store: storage, Runs: gated, Checkpoints: checkpoints})
	defer runner.Close()
	done := make(chan error, 1)
	go func() { done <- runner.RecoverRuns(ctx) }()
	<-gated.entered
	if err := storage.RunUpdateStatus(ctx, "run-x", store.RunStatusRunning); err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, exists, err := checkpoints.Get(ctx, "run-x"); err != nil || !exists {
		t.Fatalf("resumed run lost its checkpoint despite recovery CAS no-op: exists=%v err=%v", exists, err)
	}
}
