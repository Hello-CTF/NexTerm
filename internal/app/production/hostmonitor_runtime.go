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
		target := hostmonitor.Target{Runner: transport}
		// A fleet device reports its own metrics, which the sidebar shows instead; nothing is run on it.
		if _, ok := transport.(*device.Transport); ok || connected.Asset().Kind == session.KindDevice {
			target.Unsupported = "受管设备使用设备上报的指标，不在主机上采样"
			return target, nil
		}
		if hostPortsShellForGOOS(connected.Asset(), runtime.GOOS) == hostports.ShellPowerShell {
			target.Unsupported = "主机监控目前只支持 Linux 主机"
		}
		return target, nil
	})
}

func hostMonitorModule(service *hostmonitor.Service) Module {
	return Module{Name: "host-monitor", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return service.RegisterCommands(dispatcher)
	}}
}
