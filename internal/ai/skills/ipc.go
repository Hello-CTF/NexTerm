package skills

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// IPC command names for the owner-scoped skills service. No frontend
// adapter exists yet: the typed adapter lands only after the runtime
// registers these commands and the contract generation (M46) produces
// the manifest entries.
const (
	CommandList           = "ai_skills_list"
	CommandSaveOverride   = "ai_skills_save_override"
	CommandDeleteOverride = "ai_skills_delete_override"
	CommandDispatch       = "ai_skills_dispatch"
	CommandMaterial       = "ai_skills_material"
)

type nameRequest struct {
	Name string `json:"name"`
}

// RegisterIPC registers the owner-scoped skills commands on the dispatcher.
// It is the single registration entry point for the shared IPC owner: the
// agent runtime constructs the Manager (settings store, shared flag,
// embedded index) and calls RegisterIPC once during wiring. There is no
// frontend adapter until that registration and the contract generation
// (M46) have landed, so end-to-end dispatch is not claimed before then.
func RegisterIPC(dispatcher *ipc.Dispatcher, manager *Manager) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, CommandList, func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]Entry, error) {
				entries, err := manager.List(ctx)
				if err != nil {
					return nil, ipcError(err)
				}
				return entries, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandSaveOverride, func(ctx context.Context, _ *ipc.Call, input OverrideInput) (Entry, error) {
				entry, err := manager.SaveOverride(ctx, input)
				if err != nil {
					return Entry{}, ipcError(err)
				}
				return entry, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandDeleteOverride, func(ctx context.Context, _ *ipc.Call, input nameRequest) (any, error) {
				if err := manager.DeleteOverride(ctx, input.Name); err != nil {
					return nil, ipcError(err)
				}
				return nil, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandDispatch, func(ctx context.Context, _ *ipc.Call, input nameRequest) (Dispatch, error) {
				dispatch, err := manager.Dispatch(ctx, input.Name)
				if err != nil {
					return Dispatch{}, ipcError(err)
				}
				return dispatch, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandMaterial, func(ctx context.Context, _ *ipc.Call, input nameRequest) (Dispatch, error) {
				material, err := manager.Material(ctx, input.Name)
				if err != nil {
					return Dispatch{}, ipcError(err)
				}
				return material, nil
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

// ipcError maps service sentinel errors to IPC codes; anything else is
// normalized (owner-resolution failures already carry their ipc code).
func ipcError(err error) *ipc.Error {
	switch {
	case errors.Is(err, ErrSkillNotFound):
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	case errors.Is(err, ErrSkillDisabled):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	case errors.Is(err, ErrInvalidSkill):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	default:
		return ipc.NormalizeError(err)
	}
}
