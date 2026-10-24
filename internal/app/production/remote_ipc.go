package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// Remote proxy commands: the desktop calls fleet/sharing/account APIs through a logged-in server account.
// Sessions and 401 re-login are supplied by the sync.Service.Remote() proxy surface.

type remoteDeviceRequest struct {
	DeviceID string `json:"deviceId"`
}

type remoteMetricsRequest struct {
	DeviceID string `json:"deviceId"`
	SinceMS  int64  `json:"sinceMs"`
}

type remoteAutostartRequest struct {
	DeviceID string `json:"deviceId"`
	Desired  bool   `json:"desired"`
}

type remoteTerminalAccessRequest struct {
	DeviceID string `json:"deviceId"`
	Enabled  bool   `json:"enabled"`
}

type remoteEnrollCodeRequest struct {
	TTLMS  int64  `json:"ttlMs"`
	UserID string `json:"userId,omitempty"`
}

type remoteBaseURLsPutRequest struct {
	Entries []syncservice.RemoteBaseURLEntry `json:"entries"`
}

type remoteIDRequest struct {
	ID string `json:"id"`
}

type remoteAdminSetStateRequest struct {
	UserID string `json:"userId"`
	State  string `json:"state"`
}

type remoteAdminCreateUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

type remoteChangePasswordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

func registerRemoteCommands(dispatcher *ipc.Dispatcher, syncService *syncservice.Service) error {
	remote := syncService.Remote()
	badEmpty := func(field string) error {
		return ipc.NewError(ipc.CodeBadParam, field+" 不能为空")
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "remote_healthz", func(ctx context.Context, _ *ipc.Call, _ struct{}) (string, error) {
				return remote.Healthz(ctx)
			})
		},
		// fleet
		func() error {
			return ipc.Register(dispatcher, "remote_fleet_devices", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteFleetDevices, error) {
				return remote.ListDevices(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_fleet_device_metrics", func(ctx context.Context, _ *ipc.Call, input remoteMetricsRequest) (*syncservice.RemoteMetricsSamples, error) {
				if input.DeviceID == "" {
					return nil, badEmpty("deviceId")
				}
				return remote.DeviceMetrics(ctx, input.DeviceID, input.SinceMS)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_fleet_device_revoke", func(ctx context.Context, _ *ipc.Call, input remoteDeviceRequest) (syncservice.RemoteOK, error) {
				if input.DeviceID == "" {
					return syncservice.RemoteOK{}, badEmpty("deviceId")
				}
				return remote.RevokeDevice(ctx, input.DeviceID)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_fleet_device_autostart", func(ctx context.Context, _ *ipc.Call, input remoteAutostartRequest) (syncservice.RemoteAutostartResult, error) {
				if input.DeviceID == "" {
					return syncservice.RemoteAutostartResult{}, badEmpty("deviceId")
				}
				return remote.SetDeviceAutostart(ctx, input.DeviceID, input.Desired)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_fleet_device_terminal_access", func(ctx context.Context, _ *ipc.Call, input remoteTerminalAccessRequest) (syncservice.RemoteTerminalAccessResult, error) {
				if input.DeviceID == "" {
					return syncservice.RemoteTerminalAccessResult{}, badEmpty("deviceId")
				}
				return remote.SetDeviceTerminalAccess(ctx, input.DeviceID, input.Enabled)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_fleet_enroll_code", func(ctx context.Context, _ *ipc.Call, input remoteEnrollCodeRequest) (*syncservice.RemoteEnrollCode, error) {
				if input.TTLMS <= 0 {
					return nil, badEmpty("ttlMs")
				}
				return remote.CreateEnrollCode(ctx, input.TTLMS, input.UserID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "remote_fleet_base_urls_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteBaseURLs, error) {
				return remote.GetBaseURLs(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_fleet_base_urls_put", func(ctx context.Context, _ *ipc.Call, input remoteBaseURLsPutRequest) (*syncservice.RemoteBaseURLs, error) {
				return remote.PutBaseURLs(ctx, input.Entries)
			})
		},
		// sharing
		func() error {
			return ipc.Register(dispatcher, "remote_share_host_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteHostShares, error) {
				return remote.ListHostShares(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_share_host_create", func(ctx context.Context, _ *ipc.Call, input syncservice.RemoteHostShareInput) (*syncservice.RemoteHostShare, error) {
				if input.DeviceID == "" || input.RecipientUsername == "" {
					return nil, badEmpty("deviceId/recipientUsername")
				}
				return remote.CreateHostShare(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_share_host_revoke", func(ctx context.Context, _ *ipc.Call, input remoteIDRequest) (syncservice.RemoteOK, error) {
				if input.ID == "" {
					return syncservice.RemoteOK{}, badEmpty("id")
				}
				return remote.RevokeHostShare(ctx, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "remote_share_link_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteShareLinks, error) {
				return remote.ListShareLinks(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_share_link_revoke", func(ctx context.Context, _ *ipc.Call, input remoteIDRequest) (syncservice.RemoteOK, error) {
				if input.ID == "" {
					return syncservice.RemoteOK{}, badEmpty("id")
				}
				return remote.RevokeShareLink(ctx, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "remote_share_preview_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemotePreviewLinks, error) {
				return remote.ListPreviewLinks(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_share_preview_revoke", func(ctx context.Context, _ *ipc.Call, input remoteIDRequest) (syncservice.RemoteOK, error) {
				if input.ID == "" {
					return syncservice.RemoteOK{}, badEmpty("id")
				}
				return remote.RevokePreviewLink(ctx, input.ID)
			})
		},
		// account
		func() error {
			return ipc.Register(dispatcher, "remote_account_me", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteAccountMe, error) {
				return remote.Me(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "remote_account_devices", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteAccountDevices, error) {
				return remote.ListMyDevices(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_account_device_revoke", func(ctx context.Context, _ *ipc.Call, input remoteDeviceRequest) (syncservice.RemoteOK, error) {
				if input.DeviceID == "" {
					return syncservice.RemoteOK{}, badEmpty("deviceId")
				}
				return remote.RevokeMyDevice(ctx, input.DeviceID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "remote_account_admin_users", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteAccountUsers, error) {
				return remote.AdminListUsers(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_account_admin_create_user", func(ctx context.Context, _ *ipc.Call, input remoteAdminCreateUserRequest) (*syncservice.RemoteAccountUserResult, error) {
				if input.Username == "" || input.Password == "" {
					return nil, badEmpty("username/password")
				}
				return remote.AdminCreateUser(ctx, input.Username, input.Password, input.DisplayName)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_account_admin_set_user_state", func(ctx context.Context, _ *ipc.Call, input remoteAdminSetStateRequest) (syncservice.RemoteOK, error) {
				if input.UserID == "" || input.State == "" {
					return syncservice.RemoteOK{}, badEmpty("userId/state")
				}
				return remote.AdminSetUserState(ctx, input.UserID, input.State)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "remote_account_change_password", func(ctx context.Context, _ *ipc.Call, input remoteChangePasswordRequest) (*syncservice.RemotePasswordChangeResult, error) {
				if input.OldPassword == "" || input.NewPassword == "" {
					return nil, badEmpty("oldPassword/newPassword")
				}
				return remote.ChangeMyPassword(ctx, input.OldPassword, input.NewPassword)
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
