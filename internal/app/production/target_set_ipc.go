package production

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type targetSetCreateRequest struct {
	Name       string   `json:"name"`
	SessionIDs []string `json:"sessionIds"`
}

type targetSetUpdateRequest struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SessionIDs []string `json:"sessionIds"`
}

type targetSetReferenceRequest struct {
	ID string `json:"id"`
}

// registerTargetSetCommands wires the per-owner target-set CRUD and picker
// commands. The owner always comes from the authenticated context, never
// from the request payload.
//
// Registration gap: production.go does not call this yet; the hookup next to
// registerExecBatchCommands and the ipc:contract manifest regeneration that
// picks these commands up are handed to the shared owner. Target sets are
// local-only in this version: nothing here syncs.
func registerTargetSetCommands(dispatcher *ipc.Dispatcher, service *execbatch.TargetSetService) error {
	if err := ipc.Register(dispatcher, "target_set_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]store.ExecTargetSet, error) {
		sets, err := service.List(ctx, ipc.OwnerID(ctx))
		return sets, targetSetIPCError(err)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "target_set_create", func(ctx context.Context, _ *ipc.Call, input targetSetCreateRequest) (store.ExecTargetSet, error) {
		created, err := service.Create(ctx, ipc.OwnerID(ctx), input.Name, input.SessionIDs)
		return created, targetSetIPCError(err)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "target_set_get", func(ctx context.Context, _ *ipc.Call, input targetSetReferenceRequest) (store.ExecTargetSet, error) {
		set, err := service.Get(ctx, ipc.OwnerID(ctx), input.ID)
		return set, targetSetIPCError(err)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "target_set_update", func(ctx context.Context, _ *ipc.Call, input targetSetUpdateRequest) (any, error) {
		return nil, targetSetIPCError(service.Update(ctx, ipc.OwnerID(ctx), input.ID, input.Name, input.SessionIDs))
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "target_set_delete", func(ctx context.Context, _ *ipc.Call, input targetSetReferenceRequest) (any, error) {
		return nil, targetSetIPCError(service.Delete(ctx, ipc.OwnerID(ctx), input.ID))
	})
}

func targetSetIPCError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	if errors.Is(err, execbatch.ErrInvalid) {
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	}
	return ipc.NormalizeError(err)
}
