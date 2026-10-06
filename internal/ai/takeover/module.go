package takeover

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func Module(manager *Manager) app.Module {
	return app.Module{Name: "ai-takeover", RegisterCommands: manager.RegisterCommands, Component: manager}
}

type EnterResponse struct {
	Token string `json:"token"`
}

func (m *Manager) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "ai_takeover_enter", func(ctx context.Context, _ *ipc.Call, args struct {
		TabID string `json:"tabId"`
	}) (EnterResponse, error) {
		token, err := m.Enter(ctx, args.TabID)
		return EnterResponse{Token: token}, err
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "ai_takeover_run", func(ctx context.Context, call *ipc.Call, args RunArgs) (RunResponse, error) {
		args.ChannelID = call.Channel.ID
		return m.Run(ctx, args, agent.IPCStreamFactory(call.Streams))
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "ai_takeover_resume", func(ctx context.Context, call *ipc.Call, args ResumeArgs) (RunResponse, error) {
		args.ChannelID = call.Channel.ID
		return m.Resume(ctx, args, agent.IPCStreamFactory(call.Streams))
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "ai_takeover_exit", func(ctx context.Context, _ *ipc.Call, args struct {
		TabID  string `json:"tabId"`
		Token  string `json:"token"`
		Reason string `json:"reason"`
	}) (any, error) {
		return nil, m.Exit(ctx, args.TabID, args.Token, args.Reason)
	})
}
