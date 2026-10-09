package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/update"
)

type updateInstallRequest struct {
	Version string `json:"version"`
}

func registerUpdateCommands(dispatcher *ipc.Dispatcher, manager *update.Manager) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "app_update_check", func(ctx context.Context, _ *ipc.Call, _ struct{}) (update.Status, error) {
				return manager.Check(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "app_update_install", func(ctx context.Context, _ *ipc.Call, input updateInstallRequest) (update.Result, error) {
				return manager.Install(ctx, input.Version)
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
