package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

func TestExecCommandsLaterBatchFailureKeepsPartialResultsWithoutReplay(t *testing.T) {
	transport := successfulBatchTransport()
	service := batchToolService(map[string]execbatch.Target{"session-target": batchTarget(transport, "asset-target", "")}, nil)
	var batches atomic.Int32
	registry := NewRegistry(Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
			return ExecTargetSet{SessionIDs: []string{"session-target"}}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			if batches.Add(1) == 2 {
				return execbatch.Batch{}, errors.New("second batch failed")
			}
			return service.Execute(ctx, "owner-a", request)
		},
	})
	result := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "batch", Name: "exec_commands", Args: json.RawMessage(`{"commands":["echo first","echo second"],"asset_ids":["asset-target"]}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "echo first") || !strings.Contains(result.Text, "ok") ||
		!strings.Contains(result.Text, "第 2 条命令未执行") || !strings.Contains(result.Text, "此前命令已经执行") {
		t.Fatalf("partial batch result = %+v", result)
	}
	if got := transport.execCalls; len(got) != 1 || got[0] != "echo first" {
		t.Fatalf("commands replayed or skipped: %v", got)
	}
}

func TestMultiTargetNonSafeReadOnlyAndUnattendedExecuteNothing(t *testing.T) {
	for _, mode := range []guard.Mode{guard.ReadOnly, guard.Unattended} {
		t.Run(string(mode), func(t *testing.T) {
			transport := successfulBatchTransport()
			service := batchToolService(map[string]execbatch.Target{"session-target": batchTarget(transport, "asset-target", "")}, nil)
			execution := &Execution{
				JobID: "job", Scope: Scope{SessionID: "session-current"}, Permission: guard.Config{Mode: mode}, Memory: guard.NewMemory(),
				Registry: NewRegistry(Dependencies{
					ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
						return ExecTargetSet{SessionIDs: []string{"session-target"}}, nil
					},
					ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
						return service.Execute(ctx, "owner-a", request)
					},
				}),
			}
			harness := &chatGrantHarness{execution: execution}
			messages, err := harness.invoke("write", "exec_commands", `{"commands":["touch /tmp/x"],"asset_ids":["asset-target"]}`)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || !strings.Contains(messages[0].Content, `"ok":false`) {
				t.Fatalf("result = %+v", messages)
			}
			if transport.calls() != 0 {
				t.Fatalf("%s executed commands: %v", mode, transport.execCalls)
			}
		})
	}
}
