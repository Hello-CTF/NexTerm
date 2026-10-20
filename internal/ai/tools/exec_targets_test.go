package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type batchAuditRecorder struct {
	mu      sync.Mutex
	entries []store.AuditInput
}

func (b *batchAuditRecorder) AuditInsert(_ context.Context, input store.AuditInput) error {
	b.mu.Lock()
	b.entries = append(b.entries, input)
	b.mu.Unlock()
	return nil
}

func (b *batchAuditRecorder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

func successfulBatchTransport() *fakeTransport {
	code := 0
	return &fakeTransport{result: base.ExecResult{Stdout: "ok", ExitCode: &code}}
}

func batchToolService(targets map[string]execbatch.Target, auditor execbatch.Auditor) *execbatch.Service {
	return execbatch.New(execbatch.Config{
		Resolver: execbatch.ResolverFunc(func(_ context.Context, sessionID string) (execbatch.Target, error) {
			target := targets[sessionID]
			target.SessionID = sessionID
			return target, nil
		}),
		Auditor: auditor,
	})
}

func batchTarget(transport base.Transport, assetID, environment string) execbatch.Target {
	return execbatch.Target{
		SessionName: assetID, AssetID: assetID, Environment: environment,
		Transport: transport, Generation: transport.Generation(),
	}
}

func TestExecCommandsByAssetsAndTagsUsesBatchForEveryCommand(t *testing.T) {
	first := successfulBatchTransport()
	second := successfulBatchTransport()
	service := batchToolService(map[string]execbatch.Target{
		"session-a": batchTarget(first, "asset-a", ""),
		"session-b": batchTarget(second, "asset-b", ""),
	}, nil)
	var gotAssetIDs, gotTags []string
	registry := NewRegistry(Dependencies{
		ResolveExecTargets: func(_ context.Context, assetIDs, tags []string) (ExecTargetSet, error) {
			gotAssetIDs = append([]string(nil), assetIDs...)
			gotTags = append([]string(nil), tags...)
			return ExecTargetSet{SessionIDs: []string{"session-a", "session-b"}}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			return service.Execute(ctx, "owner-a", request)
		},
	})
	call := Call{ID: "batch", Name: "exec_commands", Args: json.RawMessage(`{"commands":["echo one","echo two"],"asset_ids":["asset-a","asset-b"],"tags":["Blue"]}`)}
	result := registry.Execute(context.Background(), "job", Scope{}, call, nil)
	if !result.OK {
		t.Fatalf("batch exec = %+v", result)
	}
	if strings.Join(gotAssetIDs, ",") != "asset-a,asset-b" || strings.Join(gotTags, ",") != "Blue" {
		t.Fatalf("filters = %v %v", gotAssetIDs, gotTags)
	}
	for name, transport := range map[string]*fakeTransport{"first": first, "second": second} {
		if got := transport.execCalls; strings.Join(got, ",") != "echo one,echo two" {
			t.Fatalf("%s commands = %v", name, got)
		}
	}
	if !strings.Contains(result.Text, "asset_id=asset-a") || !strings.Contains(result.Text, "asset_id=asset-b") || !strings.Contains(result.Text, "status=success") {
		t.Fatalf("batch output = %q", result.Text)
	}
}

func TestExecCommandsProductionChallengeThenConfirmedExecutesOnce(t *testing.T) {
	transport := successfulBatchTransport()
	auditor := &batchAuditRecorder{}
	service := batchToolService(map[string]execbatch.Target{
		"session-prod": batchTarget(transport, "asset-prod", "production"),
	}, auditor)
	registry := NewRegistry(Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
			return ExecTargetSet{SessionIDs: []string{"session-prod"}, Production: true}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			return service.Execute(ctx, "owner-a", request)
		},
	})
	call := Call{ID: "production", Name: "exec_commands", Args: json.RawMessage(`{"commands":["echo one","echo two"],"asset_ids":["asset-prod"]}`)}
	challenged := registry.Execute(context.Background(), "job", Scope{}, call, nil)
	if challenged.ProductionChallenge == nil || len(challenged.ProductionChallenge.Confirmations) != 2 {
		t.Fatalf("production challenge = %+v", challenged)
	}
	if transport.calls() != 0 || auditor.count() != 0 {
		t.Fatalf("unconfirmed execution happened: commands=%v audits=%d", transport.execCalls, auditor.count())
	}
	confirmed := call
	confirmed.ID += ":production"
	confirmed.OriginID = call.ID
	confirmed.ProductionConfirmations = challenged.ProductionChallenge.Confirmations
	result := registry.Execute(context.Background(), "job", Scope{}, confirmed, nil)
	if !result.OK {
		t.Fatalf("confirmed production exec = %+v", result)
	}
	if got := transport.execCalls; strings.Join(got, ",") != "echo one,echo two" {
		t.Fatalf("confirmed commands = %v", got)
	}
	if auditor.count() == 0 {
		t.Fatal("confirmed production execution was not audited")
	}
}

func TestMultiTargetAndProductionGuardsIgnoreSilentCurrentHost(t *testing.T) {
	newExecution := func(service *execbatch.Service, production bool) *Execution {
		return &Execution{
			JobID: "job", Scope: Scope{SessionID: "session-current"}, Permission: guard.Config{Mode: guard.Silent}, Memory: guard.NewMemory(),
			Registry: NewRegistry(Dependencies{
				ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
					return ExecTargetSet{SessionIDs: []string{"session-target"}, Production: production}, nil
				},
				ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
					return service.Execute(ctx, "owner-a", request)
				},
			}),
		}
	}
	t.Run("multi-target write requires confirmation", func(t *testing.T) {
		transport := successfulBatchTransport()
		service := batchToolService(map[string]execbatch.Target{"session-target": batchTarget(transport, "asset-target", "")}, nil)
		harness := &chatGrantHarness{execution: newExecution(service, false)}
		_, err := harness.invoke("write", "exec_commands", `{"commands":["touch /tmp/x"],"asset_ids":["asset-target"]}`)
		requireChatGrantInterrupt(t, err)
		if transport.calls() != 0 {
			t.Fatalf("silent mode bypassed multi-target confirmation: %v", transport.execCalls)
		}
	})
	t.Run("production challenge requires confirmation", func(t *testing.T) {
		transport := successfulBatchTransport()
		service := batchToolService(map[string]execbatch.Target{"session-target": batchTarget(transport, "asset-target", "production")}, &batchAuditRecorder{})
		harness := &chatGrantHarness{execution: newExecution(service, true)}
		_, err := harness.invoke("production", "exec_commands", `{"commands":["echo one"],"asset_ids":["asset-target"]}`)
		requireChatGrantInterrupt(t, err)
		if transport.calls() != 0 {
			t.Fatalf("silent mode bypassed production confirmation: %v", transport.execCalls)
		}
	})
}

func TestTerminalCreateAssetRequiresConfirmationEvenInSilentMode(t *testing.T) {
	terminal := &fakeTerminal{}
	execution := &Execution{
		JobID: "job", Scope: Scope{SessionID: "session-a", TabID: "tab-a"}, Permission: guard.Config{Mode: guard.Silent}, Memory: guard.NewMemory(),
		Registry: NewRegistry(Dependencies{
			Terminal: terminal,
			ResolveAssetSession: func(context.Context, string) (string, error) {
				return "session-b", nil
			},
		}),
	}
	harness := &chatGrantHarness{execution: execution}
	_, err := harness.invoke("create", "terminal_create", `{"asset_id":"asset-b"}`)
	requireChatGrantInterrupt(t, err)
	if len(terminal.createCalls) != 0 {
		t.Fatalf("silent mode bypassed asset terminal confirmation: %v", terminal.createCalls)
	}
}
