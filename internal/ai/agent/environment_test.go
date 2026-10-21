package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestConfirmRequiresTypedEnvironmentNames covers the production challenge
// end to end: the confirmRequired event carries the required names resolved
// from the real target asset, and a confirmation missing them, naming the
// wrong ones, or adding extras is rejected before any execution — the names
// are never auto-echoed from the challenge.
func TestConfirmRequiresTypedEnvironmentNames(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(ipc.WithUserID(context.Background(), "user-1"), "owned", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := hitl.NewManager(hitl.Config{
		Checkpoints: NewStoreCheckpoints(storage),
		Store:       hitlStoreBridge{storage},
		TTL:         time.Hour,
		EnvironmentResolver: func(context.Context, string) ([]string, error) {
			return []string{"production"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var actions atomic.Int64
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, manager)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("confirmRequired required names = %v", confirmation.RequiredEnvironmentNames)
	}

	// Missing names: rejected before any execution.
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	if !errors.Is(err, ErrInvalidConfirmation) || !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("missing names error = %v", err)
	}
	// Wrong names: rejected.
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"staging"}})
	if !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("wrong names error = %v", err)
	}
	// Extra names: rejected.
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production", "staging"}})
	if !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("extra names error = %v", err)
	}
	if actions.Load() != 0 {
		t.Fatalf("tool executed %d times despite rejected confirmations", actions.Load())
	}

	// The typed names resume and the tool executes exactly once.
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	if actions.Load() != 1 {
		t.Fatalf("tool actions = %d", actions.Load())
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
}
