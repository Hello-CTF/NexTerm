package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

type blockingCheckpoints struct {
	*memoryCheckpoints
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingCheckpoints() *blockingCheckpoints {
	return &blockingCheckpoints{memoryCheckpoints: NewMemoryCheckpoints(), started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingCheckpoints) Delete(ctx context.Context, id string) error {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.memoryCheckpoints.Delete(ctx, id)
}

func TestRunnerWaitBlocksUntilCheckpointCleanup(t *testing.T) {
	storage := restartStore(t)
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), tools.Dependencies{}, nil)
	checkpoints := newBlockingCheckpoints()
	runner.checkpoints = checkpoints
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.FinishedAt == nil {
		t.Fatalf("run must be finished before cleanup: %+v, %v", row, err)
	}
	<-checkpoints.started
	done := make(chan error, 1)
	go func() { done <- runner.Wait(context.Background(), response.JobID) }()
	select {
	case err := <-done:
		t.Fatalf("Wait returned during checkpoint cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if current := runner.lookupJob(response.JobID); current == nil {
		t.Fatal("job was removed before cleanup completed")
	}
	close(checkpoints.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if current := runner.lookupJob(response.JobID); current != nil {
		t.Fatal("job remained registered after cleanup")
	}
}

type blockingCloseStream struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingCloseStream) Send(context.Context, Event) error { return nil }

func (s *blockingCloseStream) Close() error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func TestRunnerWaitBlocksUntilShutdownStreamCleanup(t *testing.T) {
	runner := NewRunner(Config{})
	defer runner.Close()
	stream := &blockingCloseStream{started: make(chan struct{}), release: make(chan struct{})}
	current := &job{id: "shutdown-cleanup", ctx: context.Background(), cancel: func() {}, stream: stream, doneCh: make(chan struct{})}
	runner.jobs[current.id] = current
	runner.wg.Add(1)
	go runner.parkForShutdown(current)
	<-stream.started
	done := make(chan error, 1)
	go func() { done <- runner.Wait(context.Background(), current.id) }()
	select {
	case err := <-done:
		t.Fatalf("Wait returned during shutdown stream cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(stream.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
