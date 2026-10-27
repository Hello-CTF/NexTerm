package production

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

// localHelperStatusDTO is the local helper state shown in settings and used to
// ask for consent before an outdated helper is restarted.
type localHelperStatusDTO struct {
	State           string `json:"state"`
	Reason          string `json:"reason,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable,omitempty"`
	ActiveSessions  int    `json:"activeSessions,omitempty"`
	ActivityKnown   bool   `json:"activityKnown,omitempty"`
	HelperProtocol  int    `json:"helperProtocol,omitempty"`
	HelperVersion   string `json:"helperVersion,omitempty"`
	ConsentToken    string `json:"consentToken,omitempty"`
	Warning         string `json:"warning,omitempty"`
}

type localHelperRestartRequest struct {
	ConsentToken string `json:"consentToken"`
}

// localTerminalModeDTO is this device's choice for new local terminals:
// "durable" runs them in the local helper, "regular" keeps them in the app.
type localTerminalModeDTO struct {
	Mode string `json:"mode"`
}

// LocalTerminalModeSettingKey is a device-level setting read by the backend,
// so agent, cron and restored local terminals follow it too.
const LocalTerminalModeSettingKey = "local_helper.terminal_mode"

type localTerminalModeStore interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingSet(ctx context.Context, key, value string) error
}

func localTerminalMode(ctx context.Context, database localTerminalModeStore) (string, error) {
	if database == nil {
		return "durable", nil
	}
	value, found, err := database.SettingGet(ctx, LocalTerminalModeSettingKey)
	if err != nil {
		return "", err
	}
	if found && value == "regular" {
		return "regular", nil
	}
	return "durable", nil
}

// newProductionLocalTerminalMode resolves the device setting for the
// session manager, which keeps local terminals regular when it says so.
func newProductionLocalTerminalMode(database localTerminalModeStore) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		mode, err := localTerminalMode(ctx, database)
		if mode == "regular" {
			return session.TerminalModeRegular, err
		}
		return session.TerminalModeAuto, err
	}
}

func localHelperStatus(status localhelper.Status) localHelperStatusDTO {
	return localHelperStatusDTO{
		State: string(status.State), Reason: status.Reason, UpdateAvailable: status.UpdateAvailable,
		ActiveSessions: status.ActiveSessions, ActivityKnown: status.ActivityKnown,
		HelperProtocol: status.HelperProtocol, HelperVersion: status.HelperVersion,
		ConsentToken: status.ConsentToken, Warning: status.Warning,
	}
}

// newProductionLocalHelper returns the local helper service of a desktop or a
// single-account server, or nil when local terminals stay in-process: on
// mobile, when not requested, and on a multi-account server, where one helper
// would be shared by every account.
func newProductionLocalHelper(config ProductionConfig, singleAccount bool, logger *slog.Logger, events ipc.Emitter) *localhelper.Service {
	if !config.LocalHelper || !singleAccount || !localhelper.Supported {
		return nil
	}
	if localHelperDisabledByEnv(os.Getenv(LocalHelperEnv)) {
		logger.Info("local helper disabled by environment; local terminals stay regular", "env", LocalHelperEnv)
		return nil
	}
	executable := config.LocalHelperExecutable
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			logger.Warn("resolve executable for the local helper; local terminals stay regular", "error", err)
			return nil
		}
		executable = resolved
	}
	return localhelper.New(localhelper.Config{
		StateDir:   filepath.Join(config.DataDir, "durable", "supervisor"),
		Executable: executable,
		Logger:     logger,
		OnChange: func(status localhelper.Status) {
			if status.State == localhelper.StateFailed || status.State == localhelper.StateOutdated {
				logger.Warn("local helper needs attention", "state", status.State, "reason", status.Reason)
			}
			_ = ipc.Emit(context.Background(), events, ipc.TopicLocalHelperStatus, localHelperStatus(status))
		},
	})
}

// LocalHelperEnv set to "off" (or 0, false, no) keeps local terminals in the
// app process, for hosts where a process that outlives the app is unwanted.
const LocalHelperEnv = "NEXTERM_LOCAL_HELPER"

func localHelperDisabledByEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

func registerLocalHelperCommands(dispatcher *ipc.Dispatcher, service *localhelper.Service, database localTerminalModeStore) error {
	current := func() localHelperStatusDTO {
		if service == nil {
			return localHelperStatusDTO{State: string(localhelper.StateDisabled), Reason: "本机终端在此环境中不使用持久组件"}
		}
		return localHelperStatus(service.Status())
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "local_helper_status", func(context.Context, *ipc.Call, struct{}) (localHelperStatusDTO, error) {
				return current(), nil
			})
		},
		func() error {
			// Restarting ends every session of the outdated helper; the token
			// proves the user approved exactly the helper and count they saw.
			return ipc.Register(dispatcher, "local_helper_restart", func(ctx context.Context, _ *ipc.Call, input localHelperRestartRequest) (localHelperStatusDTO, error) {
				if service == nil {
					return localHelperStatusDTO{}, ipc.NewError(ipc.CodeUnsupported, "本机终端在此环境中不使用持久组件")
				}
				if input.ConsentToken == "" {
					return localHelperStatusDTO{}, ipc.BadParam(errors.New("缺少重启确认"))
				}
				if err := service.Restart(ctx, input.ConsentToken); err != nil {
					return localHelperStatusDTO{}, localHelperIPCError(err)
				}
				return current(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_helper_mode_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (localTerminalModeDTO, error) {
				mode, err := localTerminalMode(ctx, database)
				return localTerminalModeDTO{Mode: mode}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_helper_mode_set", func(ctx context.Context, _ *ipc.Call, input localTerminalModeDTO) (localTerminalModeDTO, error) {
				if input.Mode != "durable" && input.Mode != "regular" {
					return localTerminalModeDTO{}, ipc.BadParam(errors.New("本机终端模式无效"))
				}
				if service == nil || database == nil {
					// Without a local helper local terminals are always regular.
					return localTerminalModeDTO{}, ipc.NewError(ipc.CodeUnsupported, "本机终端在此环境中不使用持久组件")
				}
				if err := database.SettingSet(ctx, LocalTerminalModeSettingKey, input.Mode); err != nil {
					return localTerminalModeDTO{}, err
				}
				return input, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "local_helper_retry", func(ctx context.Context, _ *ipc.Call, _ struct{}) (localHelperStatusDTO, error) {
				if service == nil {
					return localHelperStatusDTO{}, ipc.NewError(ipc.CodeUnsupported, "本机终端在此环境中不使用持久组件")
				}
				if err := service.Retry(ctx); err != nil {
					return localHelperStatusDTO{}, localHelperIPCError(err)
				}
				return current(), nil
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

func localHelperIPCError(err error) error {
	switch {
	case errors.Is(err, localhelper.ErrStaleConsent):
		return ipc.WrapError(ipc.CodeNeedsConfirm, "本机持久组件的状态已变化，请重新确认后再重启", err)
	case errors.Is(err, localhelper.ErrNotOutdated):
		return ipc.WrapError(ipc.CodeBadParam, "本机持久组件当前不需要重启", err)
	case errors.Is(err, localhelper.ErrNotSupported), errors.Is(err, localhelper.ErrServiceClosed):
		return ipc.WrapError(ipc.CodeUnsupported, "本机持久组件不可用", err)
	default:
		return ipc.WrapError(ipc.Code("local_helper_restart_failed"), "重启本机持久组件失败："+err.Error(), err)
	}
}
