package forward

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
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
		return s.CreateLocal(forwardRequestContext(ctx), input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_create_socks", func(ctx context.Context, _ *ipc.Call, input CreateSocksArgs) (Spec, error) {
		return s.CreateSocks(forwardRequestContext(ctx), input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_create_remote", func(ctx context.Context, _ *ipc.Call, input CreateRemoteArgs) (Spec, error) {
		return s.CreateRemote(forwardRequestContext(ctx), input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "forward_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]Spec, error) {
		return s.ListFor(forwardRequestContext(ctx)), nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "forward_remove", func(ctx context.Context, _ *ipc.Call, input removeRequest) (any, error) {
		return nil, s.RemoveFor(forwardRequestContext(ctx), input.ID)
	})
}

func forwardRequestContext(ctx context.Context) context.Context {
	if _, ok := ipc.UserIDFromContext(ctx); ok {
		return ctx
	}
	return session.WithOpenAccess(ctx)
}
