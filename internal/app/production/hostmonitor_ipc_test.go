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

const hostMonitorWindowsOutput = "@@nexterm-monitor windows\r\n" +
	`{"os":{"mem":2048,"free":512,"swap":0,"swapFree":0,"drive":"C:"},"disks":[{"id":"C:","fs":"NTFS","size":100,"free":25}],"errors":[]}` + "\r\n"

// newHostMonitorDispatcher answers the POSIX sample script as a Linux host and the PowerShell one as a Windows
// host; commands counts the POSIX samples and windows the PowerShell ones.
func newHostMonitorDispatcher(t *testing.T) (*ipc.Dispatcher, *session.Manager, *atomic.Int32) {
	dispatcher, manager, commands, _ := newHostMonitorDispatcherWithWindows(t)
	return dispatcher, manager, commands
}

func newHostMonitorDispatcherWithWindows(t *testing.T) (*ipc.Dispatcher, *session.Manager, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var commands, windows atomic.Int32
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		code := 0
		switch command {
		case hostmonitor.SampleCommand(true):
			commands.Add(1)
			return base.ExecResult{Stdout: hostMonitorLinuxOutput, ExitCode: &code}, nil
		case hostmonitor.WindowsSampleCommand(true):
			windows.Add(1)
			return base.ExecResult{Stdout: hostMonitorWindowsOutput, ExitCode: &code}, nil
		}
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	dispatcher := ipc.NewDispatcher()
	if err := hostMonitorModule(newProductionHostMonitorService(manager)).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher, manager, &commands, &windows
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

// WinRM and SSH assets whose shell is PowerShell are sampled through PowerShell without trying sh first.
func TestHostMonitorIPCPowerShellHostsSampleThroughPowerShell(t *testing.T) {
	for name, asset := range map[string]session.Asset{
		"ssh-powershell": {ID: "asset-win", Name: "win-1", Kind: session.KindSSH, Options: map[string]any{"shell": "powershell"}},
		"winrm":          {ID: "asset-winrm", Name: "win-2", Kind: session.KindWinRM},
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher, manager, commands, windows := newHostMonitorDispatcherWithWindows(t)
			ownerA := ipc.WithUserID(t.Context(), "owner-a")
			connected, err := manager.ConnectFor(ownerA, asset)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := decodeExecBatchTest[hostmonitor.Snapshot](t, dispatchExecBatchTest(dispatcher, ownerA, "host_monitor_sample", hostmonitor.SampleArgs{SessionID: connected.ID}))
			if snapshot.Status != hostmonitor.StatusOK || snapshot.Platform != "Windows" || snapshot.Memory == nil || snapshot.Memory.UsedBytes != 1536*1024 || snapshot.Load != nil || len(snapshot.Disks) != 1 {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			if commands.Load() != 0 || windows.Load() != 1 {
				t.Fatalf("POSIX samples %d, PowerShell samples %d", commands.Load(), windows.Load())
			}
		})
	}
}

func TestHostMonitorTargetPlatform(t *testing.T) {
	tests := []struct {
		name        string
		asset       session.Asset
		device      bool
		goos        string
		platform    hostmonitor.Platform
		unsupported bool
	}{
		{name: "local windows", asset: session.Asset{Kind: session.KindLocal}, goos: "windows", platform: hostmonitor.PlatformWindows},
		{name: "local linux", asset: session.Asset{Kind: session.KindLocal}, goos: "linux", platform: hostmonitor.PlatformPOSIX},
		{name: "winrm", asset: session.Asset{Kind: session.KindWinRM}, goos: "linux", platform: hostmonitor.PlatformWindows},
		{name: "ssh powershell", asset: session.Asset{Kind: session.KindSSH, Options: map[string]any{"shell": "pwsh"}}, goos: "linux", platform: hostmonitor.PlatformWindows},
		// Windows OpenSSH defaults to cmd, so a plain SSH host is told apart by its first sample.
		{name: "ssh", asset: session.Asset{Kind: session.KindSSH}, goos: "windows", platform: hostmonitor.PlatformUnknown},
		{name: "device asset", asset: session.Asset{Kind: session.KindDevice}, goos: "linux", unsupported: true},
		{name: "device transport", asset: session.Asset{Kind: session.KindSSH}, device: true, goos: "linux", unsupported: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := hostMonitorTarget(nil, test.asset, test.device, test.goos)
			if (target.Unsupported != "") != test.unsupported || (!test.unsupported && target.Platform != test.platform) {
				t.Fatalf("target = %+v", target)
			}
		})
	}
}

func TestHostMonitorIPCDeviceSessionIsUnsupportedWithoutRunning(t *testing.T) {
	dispatcher, manager, commands := newHostMonitorDispatcher(t)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "device/dev-1", Name: "dev-1", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := decodeExecBatchTest[hostmonitor.Snapshot](t, dispatchExecBatchTest(dispatcher, ownerA, "host_monitor_sample", hostmonitor.SampleArgs{SessionID: connected.ID}))
	if snapshot.Status != hostmonitor.StatusUnsupported || commands.Load() != 0 {
		t.Fatalf("snapshot = %+v, commands %d", snapshot, commands.Load())
	}
}
