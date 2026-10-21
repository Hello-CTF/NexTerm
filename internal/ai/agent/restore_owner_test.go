package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// failOnceRunStore fails the next HitlRunSave calls while its failure
// budget lasts, then delegates to the real store.
type failOnceRunStore struct {
	*store.Store
	failures atomic.Int64
}

func (s *failOnceRunStore) HitlRunSave(ctx context.Context, id string, data []byte) error {
	if s.failures.Add(-1) >= 0 {
		return errors.New("injected hitl save failure")
	}
	return s.Store.HitlRunSave(ctx, id, data)
}

// TestRestoredJobResumesFromLegacyOwnerlessBlob covers the upgrade input:
// the hitl blob row was written with an authoritative owner, but its JSON
// predates the ownerId field. Restart recovery must seed the owner from the
// persisted row so the re-persist passes the owner guard, and the pending
// confirmation must stay resumable.
func TestRestoredJobResumesFromLegacyOwnerlessBlob(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(ipc.WithUserID(context.Background(), "user-1"), "owned", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	// Rewrite the stored blob into the pre-owner legacy format; the row
	// keeps its authoritative owner.
	ownerCtx := ipc.WithUserID(context.Background(), "user-1")
	rows, err := storage.HitlRunList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var blobData []byte
	for _, row := range rows {
		if row.ID == response.JobID {
			if row.OwnerID != "user-1" {
				t.Fatalf("blob row owner = %q", row.OwnerID)
			}
			blobData = row.Data
		}
	}
	if len(blobData) == 0 {
		t.Fatal("hitl blob not found")
	}
	var legacy map[string]any
	if err := json.Unmarshal(blobData, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "ownerId")
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.HitlRunSave(ownerCtx, response.JobID, legacyData); err != nil {
		t.Fatal(err)
	}

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatalf("recovery of legacy blob: %v", err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
}

// TestRecoverRunsRetryableRestorePersistFailure covers the data-loss half of
// the regression: a one-shot persistence failure during restore must not
// terminalize the run or delete its checkpoint. The first recovery fails
// visibly and leaves the active row, blob, and checkpoint intact with no
// ghost in-memory run; the second recovery retries the same run and the
// pending confirmation resumes.
func TestRecoverRunsRetryableRestorePersistFailure(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	failing := &failOnceRunStore{Store: storage}
	failing.failures.Store(1)
	restarted := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return sequenceModel(schema.AssistantMessage("done", nil)), 32768, nil
		},
		Tools:       tools.NewRegistry(deps),
		Store:       storage,
		Runs:        failing,
		Checkpoints: NewStoreCheckpoints(storage),
	})
	t.Cleanup(func() { _ = restarted.Close() })

	if err := restarted.RecoverRuns(context.Background()); err == nil {
		t.Fatal("recovery with a failing restore persist succeeded")
	}
	// The failed recovery kept everything the retry needs.
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.FinishedAt != nil {
		t.Fatalf("run finished by failed restore: %+v err=%v", row, err)
	}
	if _, found, err := storage.CheckpointGet(context.Background(), response.JobID); err != nil || !found {
		t.Fatalf("checkpoint deleted by failed restore: found=%v err=%v", found, err)
	}
	if blob := hitlBlobOf(t, storage, response.JobID); len(blob) == 0 {
		t.Fatal("hitl blob deleted by failed restore")
	}
	if snapshot, err := restarted.HITLSnapshot(response.JobID); err == nil {
		t.Fatalf("ghost in-memory run after failed restore: %+v", snapshot)
	}

	// The next recovery retries the same run and it resumes to completion.
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
}
