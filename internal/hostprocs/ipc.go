package hostprocs

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ProcessListArgs is the input of host_processes_list.
type ProcessListArgs struct {
	SessionID string `json:"sessionId"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "host_processes_list", func(ctx context.Context, _ *ipc.Call, input ProcessListArgs) (ProcessListResult, error) {
		return s.List(ctx, input.SessionID)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "host_process_kill", func(ctx context.Context, _ *ipc.Call, input ProcessKillArgs) (ProcessKillResult, error) {
		return s.Kill(ctx, input)
	})
}
