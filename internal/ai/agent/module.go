package agent

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// Module exposes only the agent runtime surface: chat lifecycle, HITL
// confirmation/answer, HITL reconnect replay and permission config.
// Provider, model and conversation IPC commands are owned by the production
// profiles module; registering them here as well fails composition with
// duplicate-command errors.
func Module(runner *Runner) app.Module {
	return app.Module{Name: "ai-agent", RegisterCommands: runner.RegisterCommands, Component: runnerComponent{runner: runner}}
}

func (r *Runner) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "ai_chat", func(ctx context.Context, call *ipc.Call, args ChatArgs) (StartResponse, error) {
				args.ChannelID = call.Channel.ID
				return r.Start(ctx, args, IPCStreamFactory(call.Streams))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_cancel", func(_ context.Context, _ *ipc.Call, args struct {
				JobID string `json:"jobId"`
			}) (any, error) {
				return nil, r.Cancel(args.JobID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_confirm", func(_ context.Context, _ *ipc.Call, args Confirmation) (any, error) {
				return nil, r.Confirm(args)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_answer", func(_ context.Context, _ *ipc.Call, args Answer) (any, error) {
				return nil, r.Answer(args)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_hitl_snapshot", func(_ context.Context, _ *ipc.Call, args struct {
				JobID string `json:"jobId"`
			}) (any, error) {
				return r.HITLSnapshot(args.JobID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_hitl_events", func(_ context.Context, _ *ipc.Call, args struct {
				JobID    string `json:"jobId"`
				AfterSeq uint64 `json:"afterSeq"`
			}) (any, error) {
				return r.HITLEvents(args.JobID, args.AfterSeq)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_get_permission", func(context.Context, *ipc.Call, struct{}) (guard.Config, error) {
				if r.config.Permissions == nil {
					return guard.Config{}, errors.New("权限设置未配置")
				}
				return r.config.Permissions.Get(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_set_permission", func(ctx context.Context, _ *ipc.Call, args struct {
				Config *guard.Config `json:"config"`
			}) (guard.Config, error) {
				if r.config.Permissions == nil {
					return guard.Config{}, errors.New("权限设置未配置")
				}
				if args.Config == nil {
					return guard.Config{}, errors.New("缺少 config")
				}
				if err := r.config.Permissions.Set(ctx, *args.Config); err != nil {
					return guard.Config{}, err
				}
				return r.config.Permissions.Get(), nil
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
