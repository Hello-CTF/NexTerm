package production

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	p2pclient "github.com/Hello-CTF/NexTerm/internal/p2p/client"
	"github.com/Hello-CTF/NexTerm/internal/p2p/selector"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// The connection mode of a device lives in the options of its kind=device asset row (the row that also records
// the device's group), under deviceConnectionModeOption, so it syncs with the other asset options. The path
// manager reads it from the store at every decision, never from a session snapshot.
const deviceConnectionModeOption = "p2pMode"

// deviceModeStore reads and writes the stored connection mode of devices.
type deviceModeStore struct {
	store *store.Store
}

// row returns the kind=device asset row of deviceID the asset tree uses (the oldest live one), or nil.
func (s deviceModeStore) row(ctx context.Context, deviceID string) (*store.AssetRow, map[string]any, error) {
	rows, err := s.store.AssetList(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	var found *store.AssetRow
	var options map[string]any
	for index := range rows {
		row := &rows[index]
		if row.Kind != store.AssetKindDevice {
			continue
		}
		parsed := map[string]any{}
		if row.OptionsJSON != "" {
			if err := json.Unmarshal([]byte(row.OptionsJSON), &parsed); err != nil {
				continue
			}
		}
		if id, _ := parsed["deviceId"].(string); id != deviceID {
			continue
		}
		if found == nil || row.CreatedAt < found.CreatedAt {
			found, options = row, parsed
		}
	}
	return found, options, nil
}

// Mode is the stored mode; auto when none is stored.
func (s deviceModeStore) Mode(ctx context.Context, deviceID string) (selector.Mode, error) {
	_, options, err := s.row(ctx, deviceID)
	if err != nil {
		return selector.ModeAuto, err
	}
	text, _ := options[deviceConnectionModeOption].(string)
	return selector.ParseMode(text), nil
}

// SetMode stores mode, merging it into the existing row's options or creating an ungrouped row.
func (s deviceModeStore) SetMode(ctx context.Context, deviceID, name string, mode selector.Mode) error {
	row, options, err := s.row(ctx, deviceID)
	if err != nil {
		return err
	}
	if row == nil {
		if name == "" {
			name = deviceID
		}
		encoded, err := json.Marshal(map[string]any{"deviceId": deviceID, deviceConnectionModeOption: string(mode)})
		if err != nil {
			return err
		}
		_, err = s.store.AssetCreate(ctx, store.AssetInput{Kind: store.AssetKindDevice, Name: name, OptionsJSON: string(encoded)})
		return err
	}
	if options == nil {
		options = map[string]any{}
	}
	options[deviceConnectionModeOption] = string(mode)
	encoded, err := json.Marshal(options)
	if err != nil {
		return err
	}
	text := string(encoded)
	_, err = s.store.AssetUpdate(ctx, row.ID, store.AssetPatch{OptionsJSON: &text})
	return err
}

// devicePathBackend gives the path manager the linked server and the stored modes.
type devicePathBackend struct {
	remote *syncservice.RemoteProxy
	modes  deviceModeStore
}

func (b devicePathBackend) DialRelay(ctx context.Context, deviceID, purpose string, resume bool) (net.Conn, bool, error) {
	return b.remote.DialDeviceBridgeCarrier(ctx, deviceID, purpose, resume)
}

func (b devicePathBackend) DialSignal(ctx context.Context, deviceID string) (net.Conn, error) {
	return b.remote.DialDeviceSignal(ctx, deviceID)
}

func (b devicePathBackend) Config(ctx context.Context) (*agent.P2PConfig, error) {
	return b.remote.P2PConfig(ctx)
}

func (b devicePathBackend) Grant(ctx context.Context, deviceID string, clientPub ed25519.PublicKey) (*p2pclient.Grant, error) {
	return b.remote.P2PGrant(ctx, deviceID, clientPub)
}

func (b devicePathBackend) DeviceKeyState(ctx context.Context, deviceID string) (string, error) {
	return b.remote.DeviceP2PKeyState(ctx, deviceID)
}

func (b devicePathBackend) Mode(ctx context.Context, deviceID string) (selector.Mode, error) {
	return b.modes.Mode(ctx, deviceID)
}

func (b devicePathBackend) Endpoint(ctx context.Context) (p2pclient.Endpoint, error) {
	return b.remote.P2PEndpoint(ctx)
}

// newDevicePathManager builds the desktop and mobile path manager; status changes go out on fleet://device-path.
func newDevicePathManager(remote *syncservice.RemoteProxy, database *store.Store, events ipc.Emitter, logger *slog.Logger) *p2pclient.Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return p2pclient.New(devicePathBackend{remote: remote, modes: deviceModeStore{store: database}}, p2pclient.Options{
		Logger: logger.With("component", "device-path"),
		Emit: func(status p2pclient.DevicePathStatus) {
			if events != nil {
				_ = ipc.Emit(context.Background(), events, ipc.TopicDevicePath, status)
			}
		},
	})
}

type deviceModeRequest struct {
	DeviceID string `json:"deviceId"`
	Mode     string `json:"mode"`
}

type deviceModeDTO struct {
	Mode string `json:"mode"`
}

type devicePathRequest struct {
	DeviceID string `json:"deviceId"`
}

// devicePathService answers the connection-mode and path IPC. Without a manager (the browser-mode server, where
// the server process is the bridge client) every device reports the relay with reason browser_or_share.
type devicePathService struct {
	manager *p2pclient.Manager
	modes   deviceModeStore
	// deviceName names a new device row; nil leaves the device id.
	deviceName func(ctx context.Context, deviceID string) string
}

func (s *devicePathService) relayOnly(ctx context.Context, deviceID string) p2pclient.DevicePathStatus {
	mode, _ := s.modes.Mode(ctx, deviceID)
	return p2pclient.DevicePathStatus{DeviceID: deviceID, Mode: string(mode), Path: p2pclient.PathRelay, Reason: p2pclient.ReasonBrowserOrShare}
}

func (s *devicePathService) status(ctx context.Context, deviceID string) p2pclient.DevicePathStatus {
	if s.manager == nil {
		return s.relayOnly(ctx, deviceID)
	}
	return s.manager.Status(ctx, deviceID)
}

func (s *devicePathService) diagnostics(ctx context.Context, deviceID string) p2pclient.DeviceP2PDiagnostics {
	if s.manager == nil {
		status := s.relayOnly(ctx, deviceID)
		return p2pclient.DeviceP2PDiagnostics{Status: status, LocalNAT: "unknown", RemoteNAT: "unknown", RecentSwitches: []p2pclient.PathSwitch{}, Policy: p2pclient.P2PPolicy{Mode: status.Mode}}
	}
	return s.manager.Diagnostics(ctx, deviceID)
}

func validDeviceMode(text string) (selector.Mode, bool) {
	switch selector.Mode(text) {
	case selector.ModeAuto, selector.ModeRelay, selector.ModeDirect:
		return selector.Mode(text), true
	}
	return "", false
}

func registerDevicePathCommands(dispatcher *ipc.Dispatcher, service *devicePathService) error {
	requireDevice := func(deviceID string) error {
		if strings.TrimSpace(deviceID) == "" {
			return ipc.BadParam(errors.New("deviceId 不能为空"))
		}
		return nil
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "fleet_device_connection_mode_get", func(ctx context.Context, _ *ipc.Call, input devicePathRequest) (deviceModeDTO, error) {
				if err := requireDevice(input.DeviceID); err != nil {
					return deviceModeDTO{}, err
				}
				mode, err := service.modes.Mode(ctx, input.DeviceID)
				return deviceModeDTO{Mode: string(mode)}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fleet_device_connection_mode_set", func(ctx context.Context, _ *ipc.Call, input deviceModeRequest) (deviceModeDTO, error) {
				if err := requireDevice(input.DeviceID); err != nil {
					return deviceModeDTO{}, err
				}
				mode, ok := validDeviceMode(input.Mode)
				if !ok {
					return deviceModeDTO{}, ipc.BadParam(errors.New("连接方式只能是 auto、relay 或 direct"))
				}
				name := ""
				if service.deviceName != nil {
					name = service.deviceName(ctx, input.DeviceID)
				}
				if err := service.modes.SetMode(ctx, input.DeviceID, name, mode); err != nil {
					return deviceModeDTO{}, err
				}
				if service.manager != nil {
					service.manager.ModeChanged(input.DeviceID)
				}
				return deviceModeDTO{Mode: string(mode)}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fleet_device_path_status", func(ctx context.Context, _ *ipc.Call, input devicePathRequest) (p2pclient.DevicePathStatus, error) {
				if err := requireDevice(input.DeviceID); err != nil {
					return p2pclient.DevicePathStatus{}, err
				}
				return service.status(ctx, input.DeviceID), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "fleet_device_p2p_diagnostics", func(ctx context.Context, _ *ipc.Call, input devicePathRequest) (p2pclient.DeviceP2PDiagnostics, error) {
				if err := requireDevice(input.DeviceID); err != nil {
					return p2pclient.DeviceP2PDiagnostics{}, err
				}
				return service.diagnostics(ctx, input.DeviceID), nil
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
