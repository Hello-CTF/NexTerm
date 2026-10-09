package mount

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type listRequest struct {
	ForceRefresh bool `json:"forceRefresh,omitempty"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "mount_capability", func(context.Context, *ipc.Call, struct{}) (*string, error) {
		return s.Capability(), nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "mount_list", func(ctx context.Context, _ *ipc.Call, input listRequest) ([]Entry, error) {
		return s.List(ctx, input.ForceRefresh)
	}); err != nil {
		return err
	}
	if err := ipc.RegisterNested(dispatcher, "mount_create", func(ctx context.Context, _ *ipc.Call, input CreateArgs) (Entry, error) {
		return s.Create(ctx, input)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "mount_remove", func(ctx context.Context, _ *ipc.Call, input RemoveArgs) (any, error) {
		return nil, s.Remove(ctx, input)
	})
}
