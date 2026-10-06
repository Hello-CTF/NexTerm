package forward

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type removeRequest struct {
	ID string `json:"id"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "forward_env", func(context.Context, *ipc.Call, struct{}) (Environment, error) {
		return s.Environment(), nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_create", func(ctx context.Context, _ *ipc.Call, input CreateLocalArgs) (Spec, error) {
		return s.CreateLocal(ctx, input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_create_socks", func(ctx context.Context, _ *ipc.Call, input CreateSocksArgs) (Spec, error) {
		return s.CreateSocks(ctx, input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_create_remote", func(ctx context.Context, _ *ipc.Call, input CreateRemoteArgs) (Spec, error) {
		return s.CreateRemote(ctx, input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_list", func(context.Context, *ipc.Call, struct{}) ([]Spec, error) {
		return s.List(), nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "forward_remove", func(_ context.Context, _ *ipc.Call, input removeRequest) (any, error) {
		return nil, s.Remove(input.ID)
	})
}
