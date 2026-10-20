package production

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/runbook"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type runbookHistoryReader interface {
	ExecBatchRunHistoryGet(context.Context, string) (store.ExecBatchRunHistory, error)
}

type runbookIPCService struct {
	runs    *runbook.Service
	history runbookHistoryReader
}

func newRunbookIPCService(executor runbook.Executor, history ...runbookHistoryReader) *runbookIPCService {
	var reader runbookHistoryReader
	if len(history) > 0 {
		reader = history[0]
	}
	return &runbookIPCService{runs: runbook.New(executor), history: reader}
}

func registerRunbookCommands(dispatcher *ipc.Dispatcher, batches *execbatch.Service, history *store.Store) error {
	var executor runbook.Executor
	if batches != nil {
		executor = batches
	}
	var reader runbookHistoryReader
	if history != nil {
		reader = history
	}
	return (&runbookIPCService{runs: runbook.New(executor), history: reader}).register(dispatcher)
}

func (s *runbookIPCService) register(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "runbook_run_start", func(ctx context.Context, _ *ipc.Call, input runbook.StartRequest) (runbook.RunState, error) {
		state, err := s.runs.Start(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_get", func(ctx context.Context, _ *ipc.Call, input runbook.RunRequest) (runbook.RunState, error) {
		state, err := s.runs.Get(ctx, ipc.OwnerID(ctx), input.RunID)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_prepare", func(ctx context.Context, _ *ipc.Call, input runbook.PrepareRequest) (runbook.RunState, error) {
		state, err := s.runs.Prepare(ctx, ipc.OwnerID(ctx), input)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_advance", func(ctx context.Context, _ *ipc.Call, input runbook.AdvanceRequest) (runbook.RunState, error) {
		state, err := s.runs.Advance(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_cancel", func(ctx context.Context, _ *ipc.Call, input runbook.RunRequest) (runbook.RunState, error) {
		state, err := s.runs.Cancel(ctx, ipc.OwnerID(ctx), input.RunID)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "runbook_run_history_get", func(ctx context.Context, _ *ipc.Call, input runbook.RunRequest) (store.ExecBatchRunHistory, error) {
		if s.history == nil {
			return store.ExecBatchRunHistory{}, ipc.NewError(ipc.CodeUnsupported, "Runbook 运行历史不可用")
		}
		return s.history.ExecBatchRunHistoryGet(ctx, input.RunID)
	})
}

func runbookIPCError(err error) error {
	if err == nil {
		return nil
	}
	code := ipc.CodeInternal
	message := err.Error()
	switch {
	case errors.Is(err, runbook.ErrInvalid):
		code = ipc.CodeBadParam
	case errors.Is(err, runbook.ErrNotFound):
		code = ipc.CodeNotFound
	case errors.Is(err, runbook.ErrConflict):
		code = ipc.Code("conflict")
	case errors.Is(err, runbook.ErrForbidden):
		code = ipc.CodeForbidden
	case errors.Is(err, runbook.ErrStepConfirmationRequired), errors.Is(err, runbook.ErrProductionConfirmationNeeded):
		code = ipc.CodeNeedsConfirm
	default:
		return execBatchIPCError(err)
	}
	return ipc.WrapError(code, message, err)
}
