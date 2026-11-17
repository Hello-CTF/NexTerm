package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

// A Windows target cannot run the POSIX commands exec_commands sends; it must be reported as skipped, not run and
// not hidden.
func TestExecCommandsReportsSkippedWindowsTargets(t *testing.T) {
	linux := successfulBatchTransport()
	windows := successfulBatchTransport()
	windowsTarget := batchTarget(windows, "asset-win", "")
	windowsTarget.Exec = execbatch.ExecPowerShellScript
	service := batchToolService(map[string]execbatch.Target{
		"session-linux": batchTarget(linux, "asset-linux", ""),
		"session-win":   windowsTarget,
	}, nil)
	registry := NewRegistry(Dependencies{
		ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
			return ExecTargetSet{SessionIDs: []string{"session-linux", "session-win"}}, nil
		},
		ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
			return service.Execute(ctx, "owner-a", request)
		},
	})
	call := Call{ID: "skip", Name: "exec_commands", Args: json.RawMessage(`{"commands":["uname -a"],"asset_ids":["asset-linux","asset-win"]}`)}
	result := registry.Execute(context.Background(), "job", Scope{}, call, nil)
	if windows.calls() != 0 || linux.calls() != 1 {
		t.Fatalf("windows calls=%d linux calls=%d", windows.calls(), linux.calls())
	}
	if !strings.Contains(result.Text, "asset_id=asset-win session_id=session-win status=skipped") || !strings.Contains(result.Text, "已跳过，未执行") {
		t.Fatalf("output must show the skipped target: %q", result.Text)
	}
	if !strings.Contains(result.ModelText, "skipped, not run") {
		t.Fatalf("model output must show the skipped target: %q", result.ModelText)
	}
}
