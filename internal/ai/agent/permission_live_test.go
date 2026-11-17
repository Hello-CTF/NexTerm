package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func livePermissionRunner(t *testing.T, permissions *guard.Manager) (*Runner, string) {
	t.Helper()
	transport, directory := localTransport(t)
	chat := writeCalls(t, directory)
	deps := localDeps(transport)
	deps.Terminal = ruleTerminal{assetID: "asset-a"}
	deps.TabSession = func(string) string { return "session" }
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:       tools.NewRegistry(deps),
		Store:       storage,
		Permissions: permissions,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, directory
}

func writeCalls(t *testing.T, directory string) *fakeModel {
	t.Helper()
	first, _ := json.Marshal(filepath.Join(directory, "first.txt"))
	second, _ := json.Marshal(filepath.Join(directory, "second.txt"))
	return sequenceModel(
		toolCallMessage(namedToolCall("read1", "read_file", `{"path":`+string(first)+`}`)),
		toolCallMessage(namedToolCall("write1", "write_file", `{"path":`+string(first)+`,"content":"one"}`)),
		toolCallMessage(namedToolCall("read2", "read_file", `{"path":`+string(second)+`}`)),
		toolCallMessage(namedToolCall("write2", "write_file", `{"path":`+string(second)+`,"content":"two"}`)),
		schema.AssistantMessage("done", nil),
	)
}

// A mode switch made while a run waits on a confirmation applies to the run's
// next tool call: after switching to silent, the second write runs without
// another confirmation.
func TestRunningJobAppliesModeSwitchOnNextToolCall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(context.Context, *guard.Manager) error
	}{
		{"global mode", func(ctx context.Context, manager *guard.Manager) error {
			return manager.Set(ctx, guard.Config{Mode: guard.Silent, DangerRules: []string{}})
		}},
		{"host override", func(ctx context.Context, manager *guard.Manager) error {
			return manager.SetDeviceMode(ctx, "asset-a", guard.FullAccess)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions, err := guard.NewManager(context.Background(), &ruleSettings{values: map[string]string{}})
			if err != nil {
				t.Fatal(err)
			}
			runner, directory := livePermissionRunner(t, permissions)
			stream := &SliceStream{}
			response := startScopedTestJob(t, runner, stream)

			first := waitEvent(t, stream, "confirmRequired")
			if first.ID != "write1" {
				t.Fatalf("first confirmation = %s", first.ID)
			}
			if err := tc.apply(context.Background(), permissions); err != nil {
				t.Fatal(err)
			}
			if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: first.ID, Nonce: first.Nonce, Decision: "allow"}); err != nil {
				t.Fatal(err)
			}
			events := waitClosed(t, stream)
			if confirmations := confirmEvents(events); len(confirmations) != 1 {
				t.Fatalf("confirmations = %d, want 1: the switch must apply to the running job", len(confirmations))
			}
			if result := toolResultOf(t, events, "write2"); !result.OK {
				t.Fatalf("second write = %+v", result)
			}
			if content, err := os.ReadFile(filepath.Join(directory, "second.txt")); err != nil || string(content) != "two" {
				t.Fatalf("second content = %q err=%v", content, err)
			}
		})
	}
}

// Tightening the mode mid-run applies as well: the next write is refused.
func TestRunningJobAppliesReadOnlyOnNextToolCall(t *testing.T) {
	permissions, err := guard.NewManager(context.Background(), &ruleSettings{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	runner, directory := livePermissionRunner(t, permissions)
	stream := &SliceStream{}
	response := startScopedTestJob(t, runner, stream)
	first := waitEvent(t, stream, "confirmRequired")
	if err := permissions.Set(context.Background(), guard.Config{Mode: guard.ReadOnly, DangerRules: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: first.ID, Nonce: first.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if result := toolResultOf(t, events, "write2"); result.OK {
		t.Fatalf("write after switching to read-only = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(directory, "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("write landed after switching to read-only: %v", err)
	}
}
