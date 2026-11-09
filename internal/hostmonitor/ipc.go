package hostmonitor

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// SampleArgs is the input of host_monitor_sample.
type SampleArgs struct {
	SessionID string `json:"sessionId"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	return ipc.Register(dispatcher, "host_monitor_sample", func(ctx context.Context, _ *ipc.Call, input SampleArgs) (Snapshot, error) {
		return s.Sample(ctx, input.SessionID)
	})
}
