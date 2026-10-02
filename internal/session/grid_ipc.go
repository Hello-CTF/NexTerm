package session

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	CommandTerminalResize      = "terminal_resize"
	CommandTerminalResizeFlush = "terminal_resize_flush"
)

type terminalResizeArgs struct {
	TabID    string `json:"tabId"`
	ClientID string `json:"clientId"`
	Cols     uint32 `json:"cols"`
	Rows     uint32 `json:"rows"`
}

type terminalResizeFlushArgs struct {
	TabID    string `json:"tabId"`
	ClientID string `json:"clientId"`
}

func (m *Manager) RegisterGridCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, CommandTerminalResize, func(ctx context.Context, call *ipc.Call, input terminalResizeArgs) (any, error) {
		if err := m.Resize(ctx, input.TabID, gridCommandClient(call.ClientID, input.ClientID), input.Cols, input.Rows); err != nil {
			return nil, IPCError(err)
		}
		return nil, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, CommandTerminalResizeFlush, func(ctx context.Context, call *ipc.Call, input terminalResizeFlushArgs) (any, error) {
		if err := m.FlushResize(ctx, input.TabID, gridCommandClient(call.ClientID, input.ClientID)); err != nil {
			return nil, IPCError(err)
		}
		return nil, nil
	})
}

func gridCommandClient(environmentClient, requestClient string) string {
	if requestClient != "" {
		return requestClient
	}
	return environmentClient
}
