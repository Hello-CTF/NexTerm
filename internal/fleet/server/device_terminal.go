package fleetserver

import (
	"context"
	"errors"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

// AuthorizeDeviceTerminal verifies userID's terminal access to deviceID (device owner, not revoked,
// terminal switch on) and writes a terminal_open audit, with the same rules as the bridge endpoint. Device sessions
// (session_connect_device) call it once per connection and per reconnection attempt.
func (s *Service) AuthorizeDeviceTerminal(ctx context.Context, userID, deviceID string) error {
	if userID == "" {
		return ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录")
	}
	if _, err := s.authorize(ctx, &account.Identity{UserID: userID}, deviceID, "terminal_open", auditKindDeviceTerminal); err != nil {
		return err
	}
	desired, err := s.AgentDesiredConfig(ctx, deviceID)
	if err != nil {
		return err
	}
	if !desired.TerminalEnabled {
		return ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问")
	}
	return nil
}

// TerminalDevice is a device of one account and whether its terminal helper
// can be reached through the bridge now.
type TerminalDevice struct {
	ID              string
	Name            string
	Kind            string
	Platform        string
	TerminalEnabled bool
	// Digest is the supervisor state digest of an online device; empty
	// while it is offline.
	Digest string
}

// TerminalDevices lists userID's devices that are not revoked and have an
// agent, for read-only listings of their terminal sessions. It applies the
// owner rule of canAccess and writes no audit; an empty userID lists none.
func (s *Service) TerminalDevices(ctx context.Context, userID string) ([]TerminalDevice, error) {
	if userID == "" {
		return nil, nil
	}
	devices, err := s.ListDevices(ctx, &account.Identity{UserID: userID})
	if err != nil {
		return nil, err
	}
	result := make([]TerminalDevice, 0, len(devices))
	for _, device := range devices {
		if device.RevokedAt != 0 || device.Agent == nil {
			continue
		}
		item := TerminalDevice{
			ID: device.ID, Name: device.Name, Kind: device.Kind,
			Platform: device.Agent.Platform, TerminalEnabled: device.Agent.TerminalEnabled,
		}
		if digest, ok := s.registry.StateDigest(device.ID); ok {
			item.Digest = digest
		}
		result = append(result, item)
	}
	return result, nil
}

// RequestDeviceBridge dials a bridge to the device supervisor helper for an authorized device session;
// authorization was decided at session connection time, so the bridge itself is not audited again (same semantics as registry.RequestBridge).
func (s *Service) RequestDeviceBridge(ctx context.Context, deviceID string) (net.Conn, error) {
	return s.registry.RequestBridge(ctx, deviceID, agent.PurposeTerminal)
}

// RequestDeviceBridgePurpose dials a bridge of one advertised non-terminal purpose (file/forward/listen/accept) for an authorized
// device session; authorization was decided at session connection time (same semantics as RequestDeviceBridge).
func (s *Service) RequestDeviceBridgePurpose(ctx context.Context, deviceID, purpose string) (net.Conn, error) {
	return s.registry.RequestBridge(ctx, deviceID, purpose)
}

// DeviceStateDigest returns the supervisor state digest reported by the device control channel hello;
// ok=false when the device is offline or has not reported one.
func (s *Service) DeviceStateDigest(deviceID string) (string, bool) {
	return s.registry.StateDigest(deviceID)
}

// DevicePlatform returns the agent's enrolled platform (GOOS) for the exec wrapper selection.
func (s *Service) DevicePlatform(ctx context.Context, deviceID string) (string, error) {
	deviceAgent := query.Use(s.db).DeviceAgent
	row, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	if err != nil {
		return "", dbError(err)
	}
	return row.Platform, nil
}
