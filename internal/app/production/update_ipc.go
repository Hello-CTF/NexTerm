package production

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/update"
)

type updateInstallRequest struct {
	Version string `json:"version"`
}

type updateCommandManager interface {
	Check(context.Context) update.Status
	Install(context.Context, string) (update.Result, error)
	Restart(context.Context) update.Result
}

func updateInstallIPCError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	var userErr *update.UserError
	if !errors.As(err, &userErr) {
		return err
	}
	var code ipc.Code
	switch userErr.Kind {
	case update.UserErrorBadParam:
		code = ipc.CodeBadParam
	case update.UserErrorNotFound:
		code = ipc.CodeNotFound
	case update.UserErrorIO:
		code = ipc.CodeIO
	default:
		return err
	}
	return ipc.WrapError(code, err.Error(), err)
}

func registerUpdateCommands(dispatcher *ipc.Dispatcher, manager updateCommandManager) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "app_update_check", func(ctx context.Context, _ *ipc.Call, _ struct{}) (update.Status, error) {
				return manager.Check(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "app_update_install", func(ctx context.Context, _ *ipc.Call, input updateInstallRequest) (update.Result, error) {
				result, err := manager.Install(ctx, input.Version)
				return result, updateInstallIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "app_restart", func(ctx context.Context, _ *ipc.Call, _ struct{}) (update.Result, error) {
				return manager.Restart(ctx), nil
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

type updateChannelRequest struct {
	Channel string `json:"channel"`
}

type updateChannelResponse struct {
	Channel update.ReleaseChannel `json:"channel"`
}

// registerUpdateChannelCommands exposes the per-machine release channel. The value lives in the local Setting
// table only; it is never part of the synced preferences.
func registerUpdateChannelCommands(dispatcher *ipc.Dispatcher, store update.ChannelStore) error {
	if err := ipc.Register(dispatcher, "app_update_channel_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (updateChannelResponse, error) {
		return updateChannelResponse{Channel: update.StoredChannel(ctx, store)}, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "app_update_channel_set", func(ctx context.Context, _ *ipc.Call, input updateChannelRequest) (updateChannelResponse, error) {
		channel, ok := update.ParseReleaseChannel(input.Channel)
		if !ok {
			return updateChannelResponse{}, ipc.NewError(ipc.CodeBadParam, "更新通道只能是 stable 或 rc")
		}
		if err := update.StoreChannel(ctx, store, channel); err != nil {
			return updateChannelResponse{}, ipc.WrapError(ipc.CodeDB, "保存更新通道失败: "+err.Error(), err)
		}
		return updateChannelResponse{Channel: channel}, nil
	})
}
