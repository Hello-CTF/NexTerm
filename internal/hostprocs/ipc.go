package hostprocs

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ListArgs is the input of host_processes_list.
type ListArgs struct {
	SessionID string `json:"sessionId"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "host_processes_list", func(ctx context.Context, _ *ipc.Call, input ListArgs) (ListResult, error) {
		return s.List(ctx, input.SessionID)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "host_process_kill", func(ctx context.Context, _ *ipc.Call, input KillArgs) (KillResult, error) {
		return s.Kill(ctx, input)
	})
}
