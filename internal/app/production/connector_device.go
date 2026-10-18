package production

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// DeviceBridgeDialFunc opens one bridge of the given purpose to a device, keyed by device ID:
// "terminal" reaches the supervisor helper, "file"/"forward"/"listen"/"accept" reach the agent's purpose servers.
type DeviceBridgeDialFunc func(ctx context.Context, deviceID, purpose string) (net.Conn, error)

// DeviceConnectorConfig is the dialing surface for device sessions, injected by the wiring layer per host: Dial opens a
// bridge to the device supervisor helper (desktop = WS bridge via the logged-in server account, server = fleet registry
// direct dial); Digest resolves the device supervisor status summary (server reads the control channel hello, desktop
// queries the device list); Authorize is injected server-side only and performs owner/revocation/terminal-switch checks
// with a terminal_open audit on every (re)connect, nil on desktop (every bridge is already authorized and audited per
// attempt at the server HTTP edge). Reconnects run in a background ctx where userID is unavailable, so the IPC layer
// writes the authenticated user into asset options as the fallback.
type DeviceConnectorConfig struct {
	Dial      DeviceBridgeDialFunc
	Digest    func(ctx context.Context, deviceID string) (string, error)
	Authorize func(ctx context.Context, userID, deviceID string) error
	// Platform resolves the agent's GOOS for the exec wrapper (windows selects PowerShell); nil means unknown
	// and falls back to the POSIX default.
	Platform func(ctx context.Context, deviceID string) (string, error)
}

// SetDeviceConnector injects the device session dialing surface; without it, device session connections report unsupported.
func (s *ProductionServices) SetDeviceConnector(config *DeviceConnectorConfig) {
	if s.sshConnector != nil {
		s.sshConnector.device.Store(config)
	}
}

func (c *productionConnector) connectDevice(ctx context.Context, options productionAssetOptions, generation uint64) (base.Transport, error) {
	config := c.device.Load()
	if config == nil || config.Dial == nil || config.Digest == nil {
		return nil, fmt.Errorf("%w: 设备终端通道未配置", session.ErrUnsupported)
	}
	if options.DeviceID == "" {
		return nil, ipc.BadParam(errors.New("设备会话缺少 deviceId"))
	}
	if config.Authorize != nil {
		userID, _ := ipc.UserIDFromContext(ctx)
		if userID == "" {
			userID = options.UserID
		}
		if err := config.Authorize(ctx, userID, options.DeviceID); err != nil {
			return nil, err
		}
	}
	digest, err := config.Digest(ctx, options.DeviceID)
	if err != nil {
		return nil, err
	}
	dial := func(ctx context.Context, purpose string) (net.Conn, error) {
		return config.Dial(ctx, options.DeviceID, purpose)
	}
	platform := ""
	if config.Platform != nil {
		// Best-effort: an unknown platform falls back to the POSIX exec wrapper rather than blocking the session.
		platform, _ = config.Platform(ctx, options.DeviceID)
	}
	return device.New(options.DeviceID, digest, dial, generation, platform), nil
}

// productionDurableResolver routes durable providers by transport: device transports connect directly to the device
// supervisor helper, SSH transports go through the target machine's supervisor daemon.
type productionDurableResolver struct {
	ssh    *sshdaemon.Resolver
	events ipc.Emitter
}

func (r productionDurableResolver) ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	if deviceTransport, ok := transport.(*device.Transport); ok {
		return deviceTransport.Provider(), nil
	}
	if r.ssh != nil {
		// Resolver instances are shared by all sessions. Clone the small
		// configuration value so a concurrent connection cannot overwrite the
		// progress callback for another session.
		resolver := *r.ssh
		scope := ipc.HelperProgressScopeFromContext(ctx)
		if r.events != nil && scope.AssetID != "" {
			resolver.Progress = func(progressCtx context.Context, progress sshdaemon.HelperProgress) {
				_ = ipc.Emit(progressCtx, r.events, ipc.TopicHelperProgress, ipc.HelperProgressEvent{
					SessionID:   scope.SessionID,
					AssetID:     scope.AssetID,
					Step:        progress.Step,
					Steps:       progress.Steps,
					Stage:       progress.Stage,
					Transferred: progress.Transferred,
					Total:       progress.Total,
					Percent:     progress.Percent,
					Done:        progress.Done,
					Error:       progress.Error,
				})
			}
		}
		return resolver.ResolveDurable(ctx, transport)
	}
	return nil, fmt.Errorf("%w: remote durable tabs need an SSH transport", base.ErrUnsupported)
}
