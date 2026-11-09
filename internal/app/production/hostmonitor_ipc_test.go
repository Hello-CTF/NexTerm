package production

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hostmonitor"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const hostMonitorLinuxOutput = "@@nexterm-monitor os Linux\n" +
	"@@nexterm-monitor stat\ncpu  100 0 50 1000 10 0 5\ncpu0 100 0 50 1000 10 0 5\n" +
	"@@nexterm-monitor meminfo\nMemTotal: 2048 kB\nMemAvailable: 1024 kB\n" +
	"@@nexterm-monitor loadavg\n0.10 0.20 0.30 1/50 99\n" +
	"@@nexterm-monitor end\n"

func newHostMonitorDispatcher(t *testing.T) (*ipc.Dispatcher, *session.Manager, *atomic.Int32) {
	t.Helper()
	var commands atomic.Int32
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		code := 0
		if command == hostmonitor.SampleCommand() {
			commands.Add(1)
			return base.ExecResult{Stdout: hostMonitorLinuxOutput, ExitCode: &code}, nil
		}
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	dispatcher := ipc.NewDispatcher()
	if err := hostMonitorModule(newProductionHostMonitorService(manager)).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher, manager, &commands
}

func TestHostMonitorIPCOwnerSamples(t *testing.T) {
	dispatcher, manager, commands := newHostMonitorDispatcher(t)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-ssh", Name: "web-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := decodeExecBatchTest[hostmonitor.Snapshot](t, dispatchExecBatchTest(dispatcher, ownerA, "host_monitor_sample", hostmonitor.SampleArgs{SessionID: connected.ID}))
	if snapshot.Status != hostmonitor.StatusOK || snapshot.Memory == nil || snapshot.Memory.UsedBytes != 1024*1024 || snapshot.Load == nil || commands.Load() != 1 {
		t.Fatalf("snapshot = %+v, commands %d", snapshot, commands.Load())
	}
}

func TestHostMonitorIPCOtherOwnerIsForbidden(t *testing.T) {
	dispatcher, manager, commands := newHostMonitorDispatcher(t)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-ssh", Name: "web-1", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchExecBatchTest(dispatcher, ownerB, "host_monitor_sample", hostmonitor.SampleArgs{SessionID: connected.ID})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("sample as another owner = %+v", response)
	}
	if commands.Load() != 0 {
		t.Fatalf("a forbidden request ran %d commands on the host", commands.Load())
	}
}

func TestHostMonitorIPCPowerShellHostIsUnsupportedWithoutRunning(t *testing.T) {
	dispatcher, manager, commands := newHostMonitorDispatcher(t)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-win", Name: "win-1", Kind: session.KindSSH, Options: map[string]any{"shell": "powershell"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := decodeExecBatchTest[hostmonitor.Snapshot](t, dispatchExecBatchTest(dispatcher, ownerA, "host_monitor_sample", hostmonitor.SampleArgs{SessionID: connected.ID}))
	if snapshot.Status != hostmonitor.StatusUnsupported || commands.Load() != 0 {
		t.Fatalf("snapshot = %+v, commands %d", snapshot, commands.Load())
	}
}
