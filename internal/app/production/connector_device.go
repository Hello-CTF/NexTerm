package production

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// DeviceConnectorConfig is the dialing surface for device sessions, injected by the wiring layer per host: Dial opens a
// bridge to the device supervisor helper (desktop = WS bridge via the logged-in server account, server = fleet registry
// direct dial); Digest resolves the device supervisor status summary (server reads the control channel hello, desktop
// queries the device list); Authorize is injected server-side only and performs owner/revocation/terminal-switch checks
// with a terminal_open audit on every (re)connect, nil on desktop (every bridge is already authorized and audited per
// attempt at the server HTTP edge). Reconnects run in a background ctx where userID is unavailable, so the IPC layer
// writes the authenticated user into asset options as the fallback.
type DeviceConnectorConfig struct {
	Dial      device.DialFunc
	Digest    func(ctx context.Context, deviceID string) (string, error)
	Authorize func(ctx context.Context, userID, deviceID string) error
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
	return device.New(options.DeviceID, digest, config.Dial, generation), nil
}

// productionDurableResolver routes durable providers by transport: device transports connect directly to the device
// supervisor helper, SSH transports go through the target machine's supervisor daemon.
type productionDurableResolver struct {
	ssh *sshdaemon.Resolver
}

func (r productionDurableResolver) ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	if deviceTransport, ok := transport.(*device.Transport); ok {
		return deviceTransport.Provider(), nil
	}
	if r.ssh != nil {
		return r.ssh.ResolveDurable(ctx, transport)
	}
	return nil, fmt.Errorf("%w: remote durable tabs need an SSH transport", base.ErrUnsupported)
}
