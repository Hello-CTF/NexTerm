package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func channelStreamFactory(call *ipc.Call) StreamFactory {
	if call == nil || call.Channel.ID == "" {
		return nil
	}
	base := ResilientIPCStreamFactory(call.Streams)
	return func(ctx context.Context, _, jobID string) (Stream, error) {
		return base(ctx, call.Channel.ID, jobID)
	}
}

func Module(runner *Runner) app.Module {
	return app.Module{Name: "ai-agent", RegisterCommands: runner.RegisterCommands, Component: runnerComponent{runner: runner}}
}

func (r *Runner) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "ai_chat", func(ctx context.Context, call *ipc.Call, args ChatArgs) (StartResponse, error) {
				args.ChannelID = call.Channel.ID
				return r.Start(ctx, args, ResilientIPCStreamFactory(call.Streams))
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
			return ipc.Register(dispatcher, "ai_steer", func(_ context.Context, _ *ipc.Call, args struct {
				JobID   string `json:"jobId"`
				Message string `json:"message"`
			}) (any, error) {
				return nil, r.Steer(args.JobID, args.Message)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_confirm", func(ctx context.Context, call *ipc.Call, args Confirmation) (any, error) {
				return nil, r.ConfirmStream(ctx, args, channelStreamFactory(call))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_answer", func(ctx context.Context, call *ipc.Call, args Answer) (any, error) {
				return nil, r.AnswerStream(ctx, args, channelStreamFactory(call))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_run_list", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
				Limit          int    `json:"limit"`
			}) ([]RunDTO, error) {
				return r.RunList(ctx, args.ConversationID, args.Limit)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_run_events", func(ctx context.Context, _ *ipc.Call, args struct {
				JobID    string `json:"jobId"`
				AfterSeq uint64 `json:"afterSeq"`
				Limit    int    `json:"limit"`
			}) ([]json.RawMessage, error) {
				return r.RunEventsLimit(ctx, args.JobID, args.AfterSeq, args.Limit)
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
	if r.config.Memory != nil {
		if err := r.registerMemoryCommands(dispatcher); err != nil {
			return err
		}
	}
	return nil
}
