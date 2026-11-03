package production

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hostprocs"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const hostProcsListOutput = "@@nexterm-procs ps\n" +
	"  PID  PPID USER     %CPU %MEM   RSS     ELAPSED STAT                  STARTED COMMAND\n" +
	"  812     1 app      12.5  3.2 262144       00:07 Sl   Tue Nov  3 08:10:00 2026 /usr/lib/srv/server --port 80\n" +
	"@@nexterm-procs comm\n" +
	"  812 server\n"

// These tests wire the module the way production_builder.go does (newProductionHostProcsService plus
// hostProcsModule) but do not depend on production.go registering it.
func newHostProcsDispatcher(t *testing.T) (*ipc.Dispatcher, *session.Manager) {
	t.Helper()
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		code := 0
		if strings.Contains(command, "ps -A") {
			return base.ExecResult{Stdout: hostProcsListOutput, ExitCode: &code}, nil
		}
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	dispatcher := ipc.NewDispatcher()
	if err := hostProcsModule(newProductionHostProcsService(manager, nil)).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher, manager
}

func TestHostProcsIPCOwnerCanListAndPreviewKill(t *testing.T) {
	dispatcher, manager := newHostProcsDispatcher(t)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-ssh", Name: "web-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	list := decodeExecBatchTest[hostprocs.ProcessListResult](t, dispatchExecBatchTest(dispatcher, ownerA, "host_processes_list", hostprocs.ProcessListArgs{SessionID: connected.ID}))
	if list.Platform != "posix" || len(list.Processes) != 1 || list.Processes[0].PID != 812 || list.Processes[0].Name != "server" || list.Processes[0].Started == "" {
		t.Fatalf("list = %+v", list)
	}

	kill := decodeExecBatchTest[hostprocs.ProcessKillResult](t, dispatchExecBatchTest(dispatcher, ownerA, "host_process_kill", hostprocs.ProcessKillArgs{
		SessionID: connected.ID, PID: 812, Name: "server", PPID: 1, Started: list.Processes[0].Started, DryRun: true,
	}))
	if kill.Command != "kill -TERM 812" || kill.Ran || kill.Signal != "TERM" {
		t.Fatalf("dry run = %+v", kill)
	}
}

func TestHostProcsIPCOtherOwnerIsForbidden(t *testing.T) {
	dispatcher, manager := newHostProcsDispatcher(t)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-ssh", Name: "web-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]struct {
		command string
		input   any
	}{
		"list": {"host_processes_list", hostprocs.ProcessListArgs{SessionID: connected.ID}},
		"kill": {"host_process_kill", hostprocs.ProcessKillArgs{SessionID: connected.ID, PID: 812, Name: "server", PPID: 1, DryRun: true}},
	} {
		response := dispatchExecBatchTest(dispatcher, ownerB, call.command, call.input)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
			t.Errorf("%s as another owner = %+v", name, response)
		}
	}
}
