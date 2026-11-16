package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/fs/ssh/sftptest"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func shortenFileWriteTimeout(t *testing.T) {
	t.Helper()
	previous := fileWriteTimeout
	fileWriteTimeout = time.Second
	t.Cleanup(func() { fileWriteTimeout = previous })
}

// hangingSFTPRegistry serves the AI file tools from a real SFTP server behind a proxy that can stop answering.
func hangingSFTPRegistry(t *testing.T, deps Dependencies) (*Registry, string, *sftptest.HangingServer) {
	t.Helper()
	client, root, hanging := sftptest.NewHangingClient(t, sshfs.ClientOptions()...)
	transport := &fileTransport{files: sshfs.New(client, nil)}
	deps.Transport = func(context.Context, string) (base.Transport, error) { return transport, nil }
	return NewRegistry(deps), root, hanging
}

func writeRemoteFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFileTool(registry *Registry, path string) Output {
	return registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, Call{ID: "read-" + path, Name: "read_file", Args: json.RawMessage(`{"path":"` + path + `"}`)}, nil)
}

// prepareAndExecute runs a write tool the way the agent does: prepare its diff, then execute it.
func prepareAndExecute(ctx context.Context, t *testing.T, registry *Registry, call Call) Output {
	t.Helper()
	preparation, err := registry.Prepare(ctx, "j", Scope{SessionID: "s"}, call)
	if err != nil {
		t.Fatalf("prepare %s: %v", call.Name, err)
	}
	return registry.Execute(ctx, "j", Scope{SessionID: "s"}, call, preparation)
}

func requireUnknownWriteResult(t *testing.T, result Output, tool, cause, modelCause string) {
	t.Helper()
	if result.OK || !result.OutcomeUnknown {
		t.Fatalf("%s result = %+v, want a failed result with an unknown outcome", tool, result)
	}
	requireContains(t, "display text", result.Text, "的结果未知", cause, "可能已被写入或替换，也可能没有", "read_file")
	for _, wrong := range []string{"已取消写入", "写入失败", "context canceled", "context deadline exceeded"} {
		if strings.Contains(result.Text, wrong) {
			t.Fatalf("%s result reads as a failed or cancelled write (%q): %s", tool, wrong, result.Text)
		}
	}
	projected := provider.ProjectModelOutput(tool, false, result.Text, false, false)
	requireContains(t, "model text", projected, "is unknown", modelCause, "may or may not have been written or replaced", "read_file")
}

func TestFileWriteStopsAtItsBoundWithAnUnknownOutcome(t *testing.T) {
	shortenFileWriteTimeout(t)
	for _, test := range []struct {
		name   string
		hang   sftptest.Request
		tool   string
		create bool
	}{
		{name: "staging write of an edit", hang: sftptest.Write, tool: "edit_file"},
		{name: "replacing rename of an edit", hang: sftptest.PosixRename, tool: "edit_file"},
		{name: "creating link of a new file", hang: sftptest.Hardlink, tool: "write_file", create: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, ledger, _ := outcomeFixture(t)
			registry, root, hanging := hangingSFTPRegistry(t, Dependencies{Outcome: ledger, DurableOutcomes: DurableStoreOutcomes(storage)})
			target := filepath.Join(root, "app.conf")
			args := `{"path":"` + target + `","old_string":"port=80","new_string":"port=8080"}`
			if test.create {
				args = `{"path":"` + target + `","content":"port=8080\n"}`
			} else {
				writeRemoteFixture(t, target, "port=80\n")
			}
			if read := readFileTool(registry, target); !test.create && !read.OK {
				t.Fatalf("read_file = %+v", read)
			}
			hanging.Hang(test.hang)

			call := Call{ID: "write", Name: test.tool, Args: json.RawMessage(args), AuthorizationID: "guard:test"}
			started := time.Now()
			result := prepareAndExecute(context.Background(), t, registry, call)
			if elapsed := time.Since(started); elapsed > fileWriteTimeout+5*time.Second {
				t.Fatalf("%s returned after %v", test.tool, elapsed)
			}
			requireUnknownWriteResult(t, result, test.tool, "远端在 1 秒内未答复写入步骤", "did not answer a write step within 1 seconds")

			// The read version is gone, so another write needs a fresh read_file first.
			retry := Call{ID: "retry", Name: test.tool, Args: json.RawMessage(args)}
			if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, retry); !errors.Is(err, ErrReadRequired) {
				t.Fatalf("write without a re-read after an unknown outcome = %v, want ErrReadRequired", err)
			}

			// The connection still answers reads while the abandoned step waits.
			hanging.Hang()
			read := readFileTool(registry, target)
			if test.create {
				if read.OK {
					t.Fatalf("read_file of the uncommitted new file = %+v", read)
				}
			} else if !read.OK || !strings.Contains(read.Text, "port=80") {
				t.Fatalf("read_file after the abandoned write = %+v", read)
			}

			// The ledger records the outcome as unknown, never as a plain failure or success: a replay of the call
			// says so and its operation record carries outcome_unknown.
			replayed := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, call, nil)
			requireContains(t, "replayed write", replayed.Text, "outcome=unknown", "的结果未知")
			if replayed.OK || replayed.OperationID == "" {
				t.Fatalf("replayed write = %+v", replayed)
			}
			record := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, Call{ID: "lookup", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + replayed.OperationID + `"}`)}, nil)
			requireContains(t, "operation record", record.Text, `"outcome_unknown":true`)
		})
	}
}

func TestFileWriteCancelledOnAnUnansweredStepReportsAnUnknownOutcome(t *testing.T) {
	registry, root, hanging := hangingSFTPRegistry(t, Dependencies{})
	target := filepath.Join(root, "app.conf")
	writeRemoteFixture(t, target, "port=80\n")
	if read := readFileTool(registry, target); !read.OK {
		t.Fatalf("read_file = %+v", read)
	}
	hanging.Hang(sftptest.PosixRename)
	call := Call{ID: "edit", Name: "edit_file", Args: json.RawMessage(`{"path":"` + target + `","old_string":"port=80","new_string":"port=8080"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, call)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel once the rename is known to be waiting unanswered.
	go func() {
		for held := range hanging.Held() {
			if held == sftptest.PosixRename {
				cancel()
				return
			}
		}
	}()
	done := make(chan Output, 1)
	go func() { done <- registry.Execute(ctx, "j", Scope{SessionID: "s"}, call, preparation) }()
	select {
	case result := <-done:
		requireUnknownWriteResult(t, result, "edit_file", "运行已取消时远端尚未答复写入步骤", "the run was cancelled before the remote side answered a write step")
	case <-time.After(10 * time.Second):
		t.Fatal("edit_file kept waiting on an unanswered rename after the run was cancelled")
	}
	if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, call); !errors.Is(err, ErrReadRequired) {
		t.Fatalf("edit without a re-read after a cancelled unknown write = %v, want ErrReadRequired", err)
	}
}

func TestFileWriteLookupTimeoutChangesNothing(t *testing.T) {
	previous := fileToolTimeout
	fileToolTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fileToolTimeout = previous })
	registry, root, hanging := hangingSFTPRegistry(t, Dependencies{})
	target := filepath.Join(root, "app.conf")
	writeRemoteFixture(t, target, "port=80\n")
	if read := readFileTool(registry, target); !read.OK {
		t.Fatalf("read_file = %+v", read)
	}
	call := Call{ID: "edit", Name: "edit_file", Args: json.RawMessage(`{"path":"` + target + `","old_string":"port=80","new_string":"port=8080"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, call)
	if err != nil {
		t.Fatal(err)
	}
	// The existence check before the write is a lookup: its timeout is the plain tool timeout, not an unknown outcome.
	hanging.Hang(sftptest.Stat)
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, call, preparation)
	if result.OK || result.OutcomeUnknown || !strings.Contains(result.Text, "edit_file 超时") || strings.Contains(result.Text, "结果未知") {
		t.Fatalf("edit_file with a hung lookup = %+v", result)
	}
	hanging.Hang()
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "port=80\n" {
		t.Fatalf("file after a lookup timeout = %q, %v", data, err)
	}
}
