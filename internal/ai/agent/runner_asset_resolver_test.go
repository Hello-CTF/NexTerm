package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

func assetResolverRunner(t *testing.T, chat model.BaseChatModel, deps tools.Dependencies, assetResolver func(context.Context, string, string) (string, error), environmentResolver func(context.Context, string) ([]string, error)) *Runner {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:                   func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:                   tools.NewRegistry(deps),
		Store:                   storage,
		HITLAssetResolver:       assetResolver,
		HITLEnvironmentResolver: environmentResolver,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner
}

// TestRunnerBindsRealAssetEvidenceAndEnvironmentChallenge pins the runner's
// resolver wiring end to end: the HITL interrupt carries the asset identity
// resolved from server-side state (never a client claim), the production
// challenge is resolved from that same real asset, and resume fails closed on
// an asset change or on missing/wrong typed names before any execution.
func TestRunnerBindsRealAssetEvidenceAndEnvironmentChallenge(t *testing.T) {
	var current atomic.Value
	current.Store("asset-1")
	var actions atomic.Int64
	runner := assetResolverRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error {
		actions.Add(1)
		return nil
	}}, func(context.Context, string, string) (string, error) {
		return current.Load().(string), nil
	}, func(_ context.Context, assetID string) ([]string, error) {
		if assetID == "asset-1" {
			return []string{"production"}, nil
		}
		return nil, nil
	})

	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "start the container", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("challenge names = %v, want the real asset's production names", confirmation.RequiredEnvironmentNames)
	}
	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "asset-1" {
		t.Fatalf("pending interrupt evidence = %+v, want the resolved real asset", snapshot.Pending)
	}

	// The real asset changed underneath the run: resume fails closed as stale.
	current.Store("asset-2")
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}})
	if !errors.Is(err, ErrConfirmationStale) {
		t.Fatalf("asset change resume error = %v, want stale confirmation", err)
	}
	if actions.Load() != 0 {
		t.Fatal("tool executed despite the asset mismatch")
	}

	// Back on the real asset: missing typed names fail closed before execution.
	current.Store("asset-1")
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	if !errors.Is(err, ErrInvalidConfirmation) || !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("missing names resume error = %v", err)
	}
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"staging"}})
	if !errors.Is(err, ErrInvalidConfirmation) || !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("wrong names resume error = %v", err)
	}
	if actions.Load() != 0 {
		t.Fatal("tool executed despite the wrong typed names")
	}

	// The exact typed names resume and the tool executes for real.
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	if actions.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1", actions.Load())
	}
}

// TestRunnerAssetEvidenceFailsClosedWhenUnresolvable pins the fail-closed
// boundary: a resolver that cannot prove the run's real asset identity stops
// the interrupt with a visible error instead of running unbound.
func TestRunnerAssetEvidenceFailsClosedWhenUnresolvable(t *testing.T) {
	var actions atomic.Int64
	runner := assetResolverRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error {
		actions.Add(1)
		return nil
	}}, func(context.Context, string, string) (string, error) { return "", nil }, nil)

	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{Message: "start the container", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if actions.Load() != 0 {
		t.Fatal("tool executed without asset evidence")
	}
	found := false
	for _, event := range events {
		if event.Type == "error" && strings.Contains(event.Message, hitl.ErrAssetUnknown.Error()) {
			found = true
		}
	}
	if !found {
		t.Fatalf("asset-unknown failure not surfaced: %+v", events)
	}
}

// TestRunnerWithoutResolversKeepsEvidenceUnknown pins the documented nil
// contract: with no resolvers configured, asset binding and the environment
// challenge stay disabled (evidence unknown) rather than failing the run.
func TestRunnerWithoutResolversKeepsEvidenceUnknown(t *testing.T) {
	var actions atomic.Int64
	runner := assetResolverRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error {
		actions.Add(1)
		return nil
	}}, nil, nil)

	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "start the container", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if len(confirmation.RequiredEnvironmentNames) != 0 {
		t.Fatalf("challenge names = %v, want none without a resolver", confirmation.RequiredEnvironmentNames)
	}
	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "" {
		t.Fatalf("pending interrupt evidence = %+v, want unknown asset", snapshot.Pending)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	if actions.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1", actions.Load())
	}
}
