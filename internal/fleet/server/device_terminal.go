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
	// HelperInstanceID is the random id of the helper state directory of an
	// online device; empty while offline or for agents that predate it.
	HelperInstanceID string
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
	live := make([]TerminalDevice, 0, len(devices))
	created := make(map[string]int64, len(devices))
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
		if id, ok := s.registry.HelperInstanceID(device.ID); ok {
			item.HelperInstanceID = id
		}
		created[device.ID] = device.CreatedAt
		live = append(live, item)
	}
	return dropSharedHelperDevices(live, created), nil
}

// dropSharedHelperDevices keeps one device per helper, identified by the pair
// (supervisor state digest, helper instance id). The digest only depends on
// uid and path, so different machines can share it; the instance id is a
// random per-state-dir value that tells them apart. Two devices of one
// account are the same helper only when both values are non-empty and equal
// (a desktop's local-helper exposure and an agent on that machine), and
// listing both would show every durable session twice. A device missing
// either value (an agent that predates the instance id, or an offline device)
// is never merged: at worst the same helper is listed twice, never hidden.
// The agent device is the canonical route; a local-helper device is dropped
// when an agent reports the same helper. A device with terminal access off
// cannot serve sessions, so it never displaces one that can:
// terminal-enabled devices are preferred, and a device is dropped only for a
// kept one that is enabled. Ties break by creation time, then id. The caller
// passes one account's devices, so helpers of different accounts never meet.
func dropSharedHelperDevices(devices []TerminalDevice, created map[string]int64) []TerminalDevice {
	type helperKey struct{ digest, instance string }
	keyOf := func(device TerminalDevice) (helperKey, bool) {
		return helperKey{device.Digest, device.HelperInstanceID}, device.Digest != "" && device.HelperInstanceID != ""
	}
	best := make(map[helperKey]int, len(devices))
	for i, device := range devices {
		key, ok := keyOf(device)
		if !ok {
			continue
		}
		if j, found := best[key]; !found || preferHelperRoute(device, devices[j], created) {
			best[key] = i
		}
	}
	result := make([]TerminalDevice, 0, len(devices))
	for i, device := range devices {
		if key, ok := keyOf(device); ok && best[key] != i && devices[best[key]].TerminalEnabled {
			continue
		}
		result = append(result, device)
	}
	return result
}

func preferHelperRoute(a, b TerminalDevice, created map[string]int64) bool {
	if a.TerminalEnabled != b.TerminalEnabled {
		return a.TerminalEnabled
	}
	if aLocal, bLocal := a.Kind == agent.DeviceKindLocalHelper, b.Kind == agent.DeviceKindLocalHelper; aLocal != bLocal {
		return !aLocal
	}
	if created[a.ID] != created[b.ID] {
		return created[a.ID] < created[b.ID]
	}
	return a.ID < b.ID
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

// OnlineAgents reports how many device agents hold a live control channel.
func (s *Service) OnlineAgents() int {
	if s == nil || s.registry == nil {
		return 0
	}
	return s.registry.OnlineCount()
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
