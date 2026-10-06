package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/steer"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestR35CancelFinishRaceIsIdempotent(t *testing.T) {
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")

	entered := make(chan struct{})
	release := make(chan struct{})
	cancelTestHook = func() {
		close(entered)
		<-release
	}
	defer func() { cancelTestHook = nil }()

	canceled := make(chan error, 1)
	go func() { canceled <- runner.Cancel(response.JobID) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not reach the scheduling point")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := runner.hitl.Snapshot(response.JobID)
		if err == nil && snapshot.Terminal != nil {
			if snapshot.Terminal.Reason != hitl.TerminalCanceled {
				t.Fatalf("terminal reason = %q", snapshot.Terminal.Reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not complete while cancel was parked")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatalf("cancel lost the finish race: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not return")
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
}

type failingDeleteCheckpoints struct {
	*memoryCheckpoints
	err error
}

func (s *failingDeleteCheckpoints) Delete(context.Context, string) error {
	return s.err
}

func TestR35CancelSurfacesStorageErrors(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	storeErr := errors.New("checkpoint store unavailable")
	chat := sequenceModel(toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)))
	runner := NewRunner(Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}),
		Store:       storage,
		Checkpoints: &failingDeleteCheckpoints{memoryCheckpoints: NewMemoryCheckpoints(), err: storeErr},
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "confirmRequired")
	if err := runner.Cancel(response.JobID); !errors.Is(err, storeErr) {
		t.Fatalf("cancel error = %v", err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
}

type blockingDeleteCheckpoints struct {
	*memoryCheckpoints
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingDeleteCheckpoints) Delete(ctx context.Context, id string) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.memoryCheckpoints.Delete(ctx, id)
}

func TestR35CancelStopsExecutionBeforeCheckpointCleanup(t *testing.T) {
	modelStarted := make(chan struct{})
	modelCanceled := make(chan struct{})
	var startOnce sync.Once
	var cancelOnce sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		startOnce.Do(func() { close(modelStarted) })
		<-ctx.Done()
		cancelOnce.Do(func() { close(modelCanceled) })
		return nil, ctx.Err()
	}}
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	checkpoints := &blockingDeleteCheckpoints{memoryCheckpoints: NewMemoryCheckpoints(), entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(checkpoints.release)
		}
	}()
	runner := NewRunner(Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{}),
		Store:       storage,
		Runs:        storage,
		Checkpoints: checkpoints,
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	select {
	case <-modelStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("model was not called")
	}

	canceled := make(chan error, 1)
	go func() { canceled <- runner.Cancel(response.JobID) }()
	select {
	case <-checkpoints.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not reach checkpoint cleanup")
	}
	select {
	case <-modelCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("execution was not canceled while checkpoint cleanup was blocked")
	}
	waitEvent(t, stream, "canceled")
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled {
		t.Fatalf("run row status = %q while checkpoint cleanup was blocked", row.Status)
	}
	select {
	case err := <-canceled:
		t.Fatalf("cancel returned before checkpoint cleanup finished: %v", err)
	default:
	}
	close(checkpoints.release)
	released = true
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not return")
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	snapshot, err := runner.hitl.Snapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Terminal == nil || snapshot.Terminal.Reason != hitl.TerminalCanceled {
		t.Fatalf("terminal = %+v", snapshot.Terminal)
	}
}

func TestR35CompleteJoinsRunErrorIntoCanceledRecord(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	runner := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	current := &job{
		id: "job-r35-join", ctx: context.Background(), cancel: func() {},
		deliveryCtx: context.Background(), stream: stream,
		steer: steer.NewQueue(1),
	}
	if _, err := runner.hitl.RegisterRun(context.Background(), current.id, current.id); err != nil {
		t.Fatal(err)
	}
	conversation, err := storage.ConvCreate(context.Background(), "r35", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: current.id, ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.jobs[current.id] = current
	runner.wg.Add(1)
	runner.mu.Unlock()
	if _, err := runner.hitl.Cancel(current.id); err != nil {
		t.Fatal(err)
	}
	runner.complete(current, "", 0, usage.Usage{}, errors.New("disk exploded"))
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	if last := events[len(events)-1]; last.Type != "canceled" {
		t.Fatalf("terminal event = %+v", last)
	}
	row, err := storage.RunGet(context.Background(), current.id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled {
		t.Fatalf("row status = %q", row.Status)
	}
	if !strings.Contains(row.Error, "context canceled") || !strings.Contains(row.Error, "disk exploded") {
		t.Fatalf("row error = %q", row.Error)
	}
}
