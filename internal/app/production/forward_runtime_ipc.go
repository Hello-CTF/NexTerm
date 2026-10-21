package production

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Typed IPC surface for the R-11/R-12 settings slice: per-rule autoStart
// toggles, the startup recovery switch, and the opt-in recover-now action.
// The generated TypeScript contract picks these commands up when the forward
// contract scope is regenerated; until then the backend and its behavior
// tests are the source of truth.
type forwardRuleSettingsDTO struct {
	ID         string `json:"id"`
	AssetID    string `json:"assetId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ListenHost string `json:"listenHost"`
	ListenPort int32  `json:"listenPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int32  `json:"targetPort"`
	AutoStart  bool   `json:"autoStart"`
	Status     string `json:"status"`
	SessionID  string `json:"sessionId,omitempty"`
	Error      string `json:"error,omitempty"`
}

type forwardRuleSetAutoStartRequest struct {
	ID        string `json:"id"`
	AutoStart bool   `json:"autoStart"`
}

type forwardRuleAutoStartDTO struct {
	ID        string `json:"id"`
	AutoStart bool   `json:"autoStart"`
}

type forwardRecoverySettingsDTO struct {
	Enabled                  bool `json:"enabled"`
	MaxConcurrentConnections int  `json:"maxConcurrentConnections"`
	SkipProduction           bool `json:"skipProduction"`
}

type forwardRecoverySettingsSetRequest struct {
	Enabled bool `json:"enabled"`
}

type forwardRecoverOutcomeDTO struct {
	AssetID   string   `json:"assetId"`
	SessionID string   `json:"sessionId,omitempty"`
	Started   []string `json:"started,omitempty"`
	Skipped   string   `json:"skipped,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// forwardRecoverySettingsStore persists the startup recovery switch; the
// zero value is disabled, and a corrupt file fails closed to disabled.
type forwardRecoverySettingsStore struct {
	path string
}

type forwardRecoverySettings struct {
	Enabled bool `json:"enabled"`
}

// load reads the persisted switch; any read or decode failure — including a
// type error after a partially decoded field — fails closed to the zero
// (disabled) value.
func (s *forwardRecoverySettingsStore) load() forwardRecoverySettings {
	if s == nil || s.path == "" {
		return forwardRecoverySettings{}
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return forwardRecoverySettings{}
	}
	var settings forwardRecoverySettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return forwardRecoverySettings{}
	}
	return settings
}

func (s *forwardRecoverySettingsStore) save(settings forwardRecoverySettings) error {
	if s == nil || s.path == "" {
		return nil
	}
	return atomicfile.Write(s.path, 0o600, func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(settings)
	})
}

func (r *forwardRuntime) recoverySettingsDTO() forwardRecoverySettingsDTO {
	return forwardRecoverySettingsDTO{
		Enabled:                  r.recoverySettings.load().Enabled,
		MaxConcurrentConnections: forward.MaxConcurrentRecoveryConnections,
		SkipProduction:           true,
	}
}

func (r *forwardRuntime) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "forward_rule_settings_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]forwardRuleSettingsDTO, error) {
				if r.forward == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "转发服务未配置")
				}
				rules, err := r.forward.RuleList(productionSessionRequestContext(ctx))
				if err != nil {
					return nil, err
				}
				result := make([]forwardRuleSettingsDTO, 0, len(rules))
				for _, rule := range rules {
					result = append(result, forwardRuleSettingsDTO{
						ID: rule.ID, AssetID: rule.AssetID, Name: rule.Name, Kind: rule.Kind,
						ListenHost: rule.ListenHost, ListenPort: rule.ListenPort,
						TargetHost: rule.TargetHost, TargetPort: rule.TargetPort,
						AutoStart: rule.AutoStart, Status: rule.Status,
						SessionID: rule.SessionID, Error: rule.Error,
					})
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "forward_rule_set_auto_start", func(ctx context.Context, _ *ipc.Call, input forwardRuleSetAutoStartRequest) (forwardRuleAutoStartDTO, error) {
				if r.store == nil {
					return forwardRuleAutoStartDTO{}, ipc.NewError(ipc.CodeUnsupported, "转发规则存储未配置")
				}
				row, err := r.store.ForwardRuleSetAutoStart(productionSessionRequestContext(ctx), input.ID, input.AutoStart)
				if err != nil {
					return forwardRuleAutoStartDTO{}, err
				}
				return forwardRuleAutoStartDTO{ID: row.ID, AutoStart: row.AutoStart}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "forward_rule_recovery_settings_get", func(context.Context, *ipc.Call, struct{}) (forwardRecoverySettingsDTO, error) {
				return r.recoverySettingsDTO(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "forward_rule_recovery_settings_set", func(_ context.Context, _ *ipc.Call, input forwardRecoverySettingsSetRequest) (forwardRecoverySettingsDTO, error) {
				if err := r.recoverySettings.save(forwardRecoverySettings{Enabled: input.Enabled}); err != nil {
					return forwardRecoverySettingsDTO{}, err
				}
				return r.recoverySettingsDTO(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "forward_rule_recover_now", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]forwardRecoverOutcomeDTO, error) {
				if r.closed.Load() {
					return nil, ipc.NewError(ipc.Code("conflict"), "转发运行时已关闭")
				}
				if r.forward == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "转发服务未配置")
				}
				if !r.recoverySettings.load().Enabled {
					return nil, ipc.NewError(ipc.Code("conflict"), "启动恢复未启用")
				}
				devices, err := r.recoveryDevices(ctx)
				if err != nil {
					return nil, err
				}
				outcomes := r.RecoverStartup(productionSessionRequestContext(ctx), devices)
				result := make([]forwardRecoverOutcomeDTO, 0, len(outcomes))
				for _, outcome := range outcomes {
					dto := forwardRecoverOutcomeDTO{
						AssetID: outcome.AssetID, SessionID: outcome.SessionID,
						Started: outcome.Started, Skipped: outcome.Skipped,
					}
					if outcome.Err != nil {
						dto.Error = outcome.Err.Error()
					}
					result = append(result, dto)
				}
				return result, nil
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

// recoveryDevices enumerates the caller's assets that have at least one
// auto-start rule, so recover-now stays per-device opt-in and owner-scoped.
func (r *forwardRuntime) recoveryDevices(ctx context.Context) ([]forward.RecoveryDevice, error) {
	rows, err := r.store.ForwardRuleList(productionSessionRequestContext(ctx))
	if err != nil {
		return nil, err
	}
	assetIDs := make(map[string]struct{})
	for _, row := range rows {
		if row.AutoStart {
			assetIDs[row.AssetID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(assetIDs))
	for id := range assetIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	devices := make([]forward.RecoveryDevice, 0, len(ids))
	for _, id := range ids {
		devices = append(devices, r.ConnectDeviceForRecovery(ctx, id))
	}
	return devices, nil
}
