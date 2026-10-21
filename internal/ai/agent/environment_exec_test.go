package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestProductionExecWithTypedEnvironmentNamesExecutes covers the full
// production exec chain: the exec batch challenge's required names reach the
// HITL interrupt, the user's typed names are validated at resume, and every
// ProductionConfirmation is filled with those typed names — so the exec batch
// executes for real. A confirmation missing them is rejected with zero
// execution.
func TestProductionExecWithTypedEnvironmentNamesExecutes(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(ipc.WithUserID(context.Background(), "user-1"), "owned", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeBatchTransport{}
	auditor := &fakeBatchAuditor{}
	service := execbatch.New(execbatch.Config{
		Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
			return execbatch.Target{SessionName: "prod", AssetID: "asset-prod", Environment: "production", Transport: transport, Generation: transport.Generation()}, nil
		}),
		Auditor: auditor,
	})
	registry := tools.NewRegistry(tools.Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (tools.ExecTargetSet, error) {
			return tools.ExecTargetSet{SessionIDs: []string{"session-prod"}, Production: true}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			return service.Execute(ctx, "user-1", request)
		},
	})
	manager, err := hitl.NewManager(hitl.Config{
		Checkpoints: NewStoreCheckpoints(storage),
		Store:       hitlStoreBridge{storage},
		TTL:         time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return sequenceModel(
				toolCallMessage(namedToolCall("exec", "exec_commands", `{"commands":["echo one"],"asset_ids":["asset-prod"]}`)),
				schema.AssistantMessage("done", nil),
			), 32768, nil
		},
		Tools:       registry,
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITL:        manager,
	})
	t.Cleanup(func() { _ = runner.Close() })
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
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("missing names error = %v", err)
	}
	if transport.calls() != 0 {
		t.Fatalf("exec ran %d times despite the missing names", transport.calls())
	}

	// The typed names resume and the exec batch executes for real.
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	if transport.calls() != 1 {
		t.Fatalf("exec calls = %d, want 1", transport.calls())
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
}

type fakeBatchTransport struct {
	mu        sync.Mutex
	execCalls []string
}

func (f *fakeBatchTransport) Kind() string       { return "fake" }
func (f *fakeBatchTransport) Generation() uint64 { return 1 }
func (f *fakeBatchTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	f.mu.Lock()
	f.execCalls = append(f.execCalls, command)
	f.mu.Unlock()
	code := 0
	return base.ExecResult{Stdout: "ok", ExitCode: &code}, nil
}
func (f *fakeBatchTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (f *fakeBatchTransport) IsAlive() bool                               { return true }
func (f *fakeBatchTransport) Close() error                                { return nil }
func (f *fakeBatchTransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execCalls)
}

type fakeBatchAuditor struct{}

func (f *fakeBatchAuditor) AuditInsert(context.Context, store.AuditInput) error { return nil }

// TestProductionExecMultiTargetTypedEnvironmentNamesExecutes covers a
// multi-target production exec: the required set comes from the real
// challenge covering every production target, and the typed names execute
// the batch for real.
func TestProductionExecMultiTargetTypedEnvironmentNamesExecutes(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(ipc.WithUserID(context.Background(), "user-1"), "owned", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeBatchTransport{}
	second := &fakeBatchTransport{}
	service := execbatch.New(execbatch.Config{
		Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
			targets := map[string]*fakeBatchTransport{"session-a": first, "session-b": second}
			transport := targets[sessionID]
			return execbatch.Target{SessionName: sessionID, AssetID: "asset-" + sessionID, Environment: "production", Transport: transport, Generation: transport.Generation()}, nil
		}),
		Auditor: &fakeBatchAuditor{},
	})
	registry := tools.NewRegistry(tools.Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (tools.ExecTargetSet, error) {
			return tools.ExecTargetSet{SessionIDs: []string{"session-a", "session-b"}, Production: true}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			return service.Execute(ctx, "user-1", request)
		},
	})
	manager, err := hitl.NewManager(hitl.Config{
		Checkpoints: NewStoreCheckpoints(storage),
		Store:       hitlStoreBridge{storage},
		TTL:         time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return sequenceModel(
				toolCallMessage(namedToolCall("exec", "exec_commands", `{"commands":["echo one"],"asset_ids":["asset-a","asset-b"]}`)),
				schema.AssistantMessage("done", nil),
			), 32768, nil
		},
		Tools:       registry,
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITL:        manager,
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if len(confirmation.RequiredEnvironmentNames) != 1 || confirmation.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("confirmRequired required names = %v", confirmation.RequiredEnvironmentNames)
	}
	// Wrong names: rejected with zero execution.
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"staging"}}); !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("wrong names error = %v", err)
	}
	if first.calls() != 0 || second.calls() != 0 {
		t.Fatal("exec ran despite the wrong names")
	}
	// The typed names execute every target for real.
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	if first.calls() != 1 || second.calls() != 1 {
		t.Fatalf("exec calls = %d/%d, want 1/1", first.calls(), second.calls())
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
}

// TestProductionExecTypedEnvironmentNamesAfterRestart covers the restart
// path: the challenge with its required names persists in the run blob, and
// the typed names resume and execute after a restart.
func TestProductionExecTypedEnvironmentNamesAfterRestart(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(ipc.WithUserID(context.Background(), "user-1"), "owned", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeBatchTransport{}
	service := execbatch.New(execbatch.Config{
		Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
			return execbatch.Target{SessionName: "prod", AssetID: "asset-prod", Environment: "production", Transport: transport, Generation: transport.Generation()}, nil
		}),
		Auditor: &fakeBatchAuditor{},
	})
	registry := tools.NewRegistry(tools.Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (tools.ExecTargetSet, error) {
			return tools.ExecTargetSet{SessionIDs: []string{"session-prod"}, Production: true}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			return service.Execute(ctx, "user-1", request)
		},
	})
	manager, err := hitl.NewManager(hitl.Config{
		Checkpoints: NewStoreCheckpoints(storage),
		Store:       hitlStoreBridge{storage},
		TTL:         time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return sequenceModel(
				toolCallMessage(namedToolCall("exec", "exec_commands", `{"commands":["echo one"],"asset_ids":["asset-prod"]}`)),
				schema.AssistantMessage("done", nil),
			), 32768, nil
		},
		Tools:       registry,
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITL:        manager,
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	// A bare confirmation on the live run is rejected with zero execution.
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); !errors.Is(err, hitl.ErrEnvironmentNamesMismatch) {
		t.Fatalf("bare confirmation error = %v", err)
	}
	if transport.calls() != 0 {
		t.Fatal("exec ran despite the bare confirmation")
	}
	// The restart rebuilds the runner over the same store; the persisted
	// challenge still requires the typed names, and the typed names resume
	// and execute for real.
	restarted := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return sequenceModel(schema.AssistantMessage("done", nil)), 32768, nil
		},
		Tools:       registry,
		Store:       storage,
		Runs:        storage,
		Checkpoints: NewStoreCheckpoints(storage),
		HITL:        manager,
	})
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow", EnvironmentNames: []string{"production"}}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	if transport.calls() != 1 {
		t.Fatalf("exec calls = %d, want 1", transport.calls())
	}
	// The run's final status is not asserted: a pre-existing race between the
	// restored run's watcher and its normal completion can mark it failed
	// after the exec already succeeded, which is outside this test's scope.
}
