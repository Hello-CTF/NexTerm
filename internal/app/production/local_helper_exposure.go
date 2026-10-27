package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/fleet/exposure"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const (
	// LocalHelperExposeSettingKey is the user's choice to expose this
	// desktop's local terminals to its server account; it is off by default.
	LocalHelperExposeSettingKey = "local_helper.expose"
	// localHelperExposureEnrollmentKey holds the device credentials of the
	// exposure, sealed by the credential vault.
	localHelperExposureEnrollmentKey = "fleet.local_helper_enrollment"
	// localHelperExposureEnrollCodeTTL bounds the enroll code the desktop
	// issues for itself and consumes right away.
	localHelperExposureEnrollCodeTTL = 2 * time.Minute
	// localHelperExposureEnsureTimeout bounds the wait for the local helper
	// when the server asks for a terminal bridge.
	localHelperExposureEnsureTimeout = 30 * time.Second
)

type localHelperExposureSettings interface {
	localTerminalModeStore
	SettingDelete(ctx context.Context, key string) error
}

type localHelperExposureSecrets interface {
	Status() vault.Status
	EncryptSecret(ctx context.Context, plaintext string) (string, error)
	DecryptSecret(ctx context.Context, envelope string) (string, error)
}

type localHelperExposureAccount interface {
	LinkGet(ctx context.Context) (syncservice.Link, error)
	Remote() *syncservice.RemoteProxy
}

// localHelperExposureHost connects the exposure controller to the desktop's
// settings, credential vault, sync link and local helper.
type localHelperExposureHost struct {
	settings localHelperExposureSettings
	secrets  localHelperExposureSecrets
	account  localHelperExposureAccount
	helper   *localhelper.Service
}

func (h *localHelperExposureHost) Enabled(ctx context.Context) (bool, error) {
	value, found, err := h.settings.SettingGet(ctx, LocalHelperExposeSettingKey)
	return err == nil && found && value == "on", err
}

func (h *localHelperExposureHost) SetEnabled(ctx context.Context, enabled bool) error {
	value := "off"
	if enabled {
		value = "on"
	}
	return h.settings.SettingSet(ctx, LocalHelperExposeSettingKey, value)
}

func (h *localHelperExposureHost) Blocked(ctx context.Context) string {
	return localHelperExposureBlocked(ctx, h.helper, h.settings)
}

func localHelperExposureBlocked(ctx context.Context, helper *localhelper.Service, settings localTerminalModeStore) string {
	if helper == nil {
		return "本机终端在此环境中不使用持久组件，无法开放给服务器"
	}
	mode, err := localTerminalMode(ctx, settings)
	if err != nil {
		return "无法读取本机终端模式：" + err.Error()
	}
	if mode == "regular" {
		return "本机终端为普通模式，不向服务器开放"
	}
	return ""
}

func (h *localHelperExposureHost) Account(ctx context.Context) (exposure.Account, error) {
	link, err := h.account.LinkGet(vault.WithBackground(ctx))
	if err != nil {
		return exposure.Account{}, exposureVaultError(err)
	}
	if !link.IsConfigured() {
		return exposure.Account{}, exposure.ErrNoAccount
	}
	return exposure.Account{URL: link.URL, Username: link.Username, Insecure: link.Insecure}, nil
}

func exposureVaultError(err error) error {
	var appErr *ipc.Error
	if errors.As(err, &appErr) && (appErr.Code == ipc.CodeVaultLocked || appErr.Code == ipc.CodeVaultNotInit) {
		return fmt.Errorf("%w: %v", exposure.ErrLocked, err)
	}
	return err
}

func (h *localHelperExposureHost) LoadEnrollment(ctx context.Context) (*exposure.Enrollment, error) {
	value, found, err := h.settings.SettingGet(ctx, localHelperExposureEnrollmentKey)
	if err != nil {
		return nil, err
	}
	if !found || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	plaintext, err := h.secrets.DecryptSecret(vault.WithBackground(ctx), value)
	if err != nil {
		return nil, exposureVaultError(err)
	}
	var enrollment exposure.Enrollment
	if err := json.Unmarshal([]byte(plaintext), &enrollment); err != nil {
		return nil, fmt.Errorf("本机设备凭据损坏: %w", err)
	}
	return &enrollment, nil
}

func (h *localHelperExposureHost) SaveEnrollment(ctx context.Context, enrollment *exposure.Enrollment) error {
	plaintext, err := json.Marshal(enrollment)
	if err != nil {
		return err
	}
	sealed, err := h.secrets.EncryptSecret(vault.WithBackground(ctx), string(plaintext))
	if err != nil {
		return exposureVaultError(err)
	}
	return h.settings.SettingSet(ctx, localHelperExposureEnrollmentKey, sealed)
}

func (h *localHelperExposureHost) RemoveEnrollment(ctx context.Context) error {
	return h.settings.SettingDelete(ctx, localHelperExposureEnrollmentKey)
}

// Enroll issues an enroll code through the logged-in account and consumes it
// at once, so the device belongs to exactly that account.
func (h *localHelperExposureHost) Enroll(ctx context.Context, account exposure.Account, request agent.EnrollRequest) (*agent.EnrollResponse, error) {
	code, err := h.account.Remote().CreateEnrollCode(ctx, localHelperExposureEnrollCodeTTL.Milliseconds(), "")
	if err != nil {
		return nil, err
	}
	client, err := agent.NewEndpointClient(agent.BaseURLEntry{URL: account.URL, Insecure: account.Insecure})
	if err != nil {
		return nil, err
	}
	request.Code = code.Code
	return client.Enroll(ctx, request)
}

func (h *localHelperExposureHost) Revoke(ctx context.Context, deviceID string) error {
	_, err := h.account.Remote().RevokeDevice(ctx, deviceID)
	return err
}

// newLocalHelperExposureRunner builds the in-process device agent that
// bridges the server to the local helper. It serves terminal bridges only and
// refuses them while local terminals are regular; a bridge for a durable local
// terminal may start the helper, as opening one on this desktop would.
func newLocalHelperExposureRunner(dataDir string, helper *localhelper.Service, settings localTerminalModeStore, logger *slog.Logger) exposure.RunnerFactory {
	return func(config *agent.Config, store agent.ConfigStore, onChannel func(bool)) (exposure.Runner, error) {
		prober, err := agent.NewProber(config.BaseURLs)
		if err != nil {
			return nil, err
		}
		return agent.New(agent.Options{
			Store:    store,
			DataDir:  dataDir,
			Prober:   prober,
			Manager:  agent.EmbeddedServiceManager(),
			StateDir: helper.StateDir(),
			EnsureHelper: func(ctx context.Context) error {
				if reason := localHelperExposureBlocked(ctx, helper, settings); reason != "" {
					return errors.New(reason)
				}
				_, err := helper.WaitReady(ctx, localHelperExposureEnsureTimeout)
				return err
			},
			NoSelfUpgrade: true,
			OnChannel:     onChannel,
			Logger:        logger.With("component", "local-helper-exposure"),
		})
	}
}

// newProductionLocalHelperExposure returns the exposure controller of a
// desktop with a local helper, or nil.
func newProductionLocalHelperExposure(desktop bool, dataDir, appVersion string, helper *localhelper.Service, settings localHelperExposureSettings, secrets *vault.Vault, account *syncservice.Service, logger *slog.Logger) *exposure.Controller {
	if !desktop || helper == nil || settings == nil || secrets == nil || account == nil {
		return nil
	}
	controller := exposure.New(exposure.Config{
		Host:       &localHelperExposureHost{settings: settings, secrets: secrets, account: account, helper: helper},
		NewRunner:  newLocalHelperExposureRunner(dataDir, helper, settings, logger),
		AppVersion: appVersion,
		Logger:     logger,
		OnChange: func(status exposure.Status) {
			logger.Info("local terminal exposure", "state", status.State, "reason", status.Reason, "device_id", status.DeviceID)
		},
	})
	secrets.AddUnlockListener(controller.Reconcile)
	return controller
}

type localHelperExposeRequest struct {
	Enabled bool `json:"enabled"`
}

// localHelperExposureDTO is the exposure state shown in settings: whether
// this desktop's local terminals are reachable from its server account.
type localHelperExposureDTO struct {
	Enabled  bool   `json:"enabled"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	Server   string `json:"server,omitempty"`
}

func localHelperExposureStatus(status exposure.Status) localHelperExposureDTO {
	return localHelperExposureDTO{
		Enabled: status.Enabled, State: string(status.State), Reason: status.Reason, DeviceID: status.DeviceID, Server: status.Server,
	}
}

func registerLocalHelperExposureCommands(dispatcher *ipc.Dispatcher, controller *exposure.Controller) error {
	unsupported := localHelperExposureDTO{State: string(exposure.StateUnavailable), Reason: "此环境不能向服务器开放本机终端"}
	if err := ipc.Register(dispatcher, "local_helper_expose_get", func(context.Context, *ipc.Call, struct{}) (localHelperExposureDTO, error) {
		if controller == nil {
			return unsupported, nil
		}
		return localHelperExposureStatus(controller.Status()), nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "local_helper_expose_set", func(ctx context.Context, _ *ipc.Call, input localHelperExposeRequest) (localHelperExposureDTO, error) {
		if controller == nil {
			return localHelperExposureDTO{}, ipc.NewError(ipc.CodeUnsupported, unsupported.Reason)
		}
		status, err := controller.SetEnabled(ctx, input.Enabled)
		if err != nil {
			return localHelperExposureDTO{}, ipc.WrapError(ipc.Code("local_helper_expose_failed"), err.Error(), err)
		}
		return localHelperExposureStatus(status), nil
	})
}
