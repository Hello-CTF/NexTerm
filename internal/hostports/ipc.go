package hostports

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// ListArgs is the input of host_ports_list.
type ListArgs struct {
	SessionID string `json:"sessionId"`
	Processes bool   `json:"processes"`
	// QuietMs skips the listing while the terminal produced output more recently than this.
	QuietMs int `json:"quietMs,omitempty"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "host_ports_list", func(ctx context.Context, _ *ipc.Call, input ListArgs) (ListResult, error) {
		return s.ListWithQuiet(ctx, input.SessionID, input.Processes, time.Duration(max(input.QuietMs, 0))*time.Millisecond)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "host_connectivity_test", func(ctx context.Context, _ *ipc.Call, input ProbeArgs) (ProbeResult, error) {
		return s.Probe(ctx, input)
	})
}
