package production

import (
	"context"
	"runtime"

	"github.com/Hello-CTF/NexTerm/internal/hostmonitor"
	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// newProductionHostMonitorService samples host metrics on the session's exec channel, after the caller has
// been authorized for the session. The command runs over the session's existing connection.
func newProductionHostMonitorService(sessions *session.Manager) *hostmonitor.Service {
	return hostmonitor.NewService(func(ctx context.Context, sessionID string) (hostmonitor.Target, error) {
		ctx = productionSessionRequestContext(ctx)
		connected, err := sessions.SessionFor(ctx, sessionID)
		if err != nil {
			return hostmonitor.Target{}, session.IPCError(err)
		}
		transport, err := sessions.TransportFor(ctx, sessionID)
		if err != nil {
			return hostmonitor.Target{}, session.IPCError(err)
		}
		_, isDevice := transport.(*device.Transport)
		return hostMonitorTarget(transport, connected.Asset(), isDevice, runtime.GOOS), nil
	})
}

// hostMonitorTarget picks the sample script for the session. A fleet device reports its own metrics, which the
// sidebar shows instead, so nothing is run on it. WinRM, a local session on Windows and an SSH asset whose shell
// is PowerShell are sampled through PowerShell; other SSH hosts are told apart by the first sample.
func hostMonitorTarget(runner hostmonitor.Runner, asset session.Asset, isDevice bool, goos string) hostmonitor.Target {
	target := hostmonitor.Target{Runner: runner}
	if isDevice || asset.Kind == session.KindDevice {
		target.Unsupported = "受管设备使用设备上报的指标，不在主机上采样"
		return target
	}
	switch hostPortsShellForGOOS(asset, goos) {
	case hostports.ShellPowerShell:
		target.Platform = hostmonitor.PlatformWindows
	case hostports.ShellPOSIX:
		target.Platform = hostmonitor.PlatformPOSIX
	}
	return target
}

func hostMonitorModule(service *hostmonitor.Service) Module {
	return Module{Name: "host-monitor", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return service.RegisterCommands(dispatcher)
	}}
}
