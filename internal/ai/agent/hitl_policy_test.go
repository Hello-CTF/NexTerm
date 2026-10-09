package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

func hitlPolicyConfig(storage *store.Store, ttl time.Duration) Config {
	return Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return dockerConfirmChat(), 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}),
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITLTTL:     ttl,
	}
}

func TestRunnerHITLTTLConfigDrivesLiveExpiry(t *testing.T) {
	storage := restartStore(t)
	runner := NewRunner(hitlPolicyConfig(storage, 80*time.Millisecond))
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitEvent(t, stream, "confirmRequired")

	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if window := snapshot.Pending[0].ExpiresAt.Sub(snapshot.Pending[0].CreatedAt); window != 80*time.Millisecond {
		t.Fatalf("configured TTL did not reach the hitl manager: expiry window = %v", window)
	}

	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	for _, event := range events {
		if event.Type == "error" && !strings.Contains(event.Message, "过期") {
			t.Fatalf("terminal error does not report expiry: %+v", event)
		}
	}
	final, err := runner.HITLSnapshot(response.JobID)
	if err != nil || final.Status != hitl.RunStatusExpired || final.Terminal == nil || final.Terminal.Reason != hitl.TerminalExpired {
		t.Fatalf("final snapshot = %+v err=%v", final, err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
}

func TestRunnerHITLTTLConfigSurvivesRestartRecovery(t *testing.T) {
	storage := restartStore(t)
	runner := NewRunner(hitlPolicyConfig(storage, 150*time.Millisecond))
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := NewRunner(hitlPolicyConfig(storage, 150*time.Millisecond))
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
	if !strings.Contains(row.Error, "过期") {
		t.Fatalf("recovered row = %+v", row)
	}
	snapshot, err := restarted.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal == nil || snapshot.Terminal.Reason != hitl.TerminalExpired {
		t.Fatalf("restored snapshot = %+v err=%v", snapshot, err)
	}
	if err := restarted.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err == nil {
		t.Fatal("expired confirmation accepted after restart")
	}
}
