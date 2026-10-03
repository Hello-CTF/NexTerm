package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestR35CancelFinishRaceIsIdempotent deterministically reproduces the
// cancel-versus-finish race: the job-context cancel inside Runner.Cancel
// makes the run's own completion reach the HITL manager first, so the
// manager cancel then reports ErrRunFinished. Cancel must confirm the
// terminal state and succeed idempotently instead of surfacing the race.
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

// TestR35CancelSurfacesStorageErrors verifies that a checkpoint cleanup
// failure during cancel reaches the caller instead of being concealed as
// idempotent success.
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
