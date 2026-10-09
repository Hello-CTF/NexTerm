package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// guardGrantMutation 在服务端装配(ServerMode)下把设备授权/授权规则的变更收敛为
// 超管: 授权状态是全局设置, 普通用户修改等于替全体用户授权。桌面端与无身份的
// 开放部署(auth=off / loopback 免登录)保持原行为。
func (r *Runner) guardGrantMutation(ctx context.Context) error {
	if !r.config.ServerMode {
		return nil
	}
	if _, ok := ipc.UserIDFromContext(ctx); !ok {
		return nil
	}
	if role, _ := ipc.RoleFromContext(ctx); role != string(account.RoleSuperadmin) {
		return ipc.NewError(ipc.CodeForbidden, "需要超管权限")
	}
	return nil
}

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
			return ipc.Register(dispatcher, "ai_edit_resend", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
				MessageID      string `json:"messageId"`
			}) (any, error) {
				return nil, r.EditResend(ctx, args.ConversationID, args.MessageID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_conversation_retitle", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
			}) (string, error) {
				return r.RetitleConversation(ctx, args.ConversationID)
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
		func() error {
			return ipc.Register(dispatcher, "ai_grant_list", func(context.Context, *ipc.Call, struct{}) ([]guard.DeviceGrant, error) {
				if r.config.Grants == nil {
					return nil, errors.New("设备授权未配置")
				}
				return r.config.Grants.List(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_set", func(ctx context.Context, _ *ipc.Call, args struct {
				DeviceID string   `json:"deviceId"`
				Kinds    []string `json:"kinds"`
			}) (guard.DeviceGrant, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return guard.DeviceGrant{}, err
				}
				if r.config.Grants == nil {
					return guard.DeviceGrant{}, errors.New("设备授权未配置")
				}
				kinds, err := guard.ParseGrantKinds(args.Kinds)
				if err != nil {
					return guard.DeviceGrant{}, err
				}
				return r.config.Grants.Grant(ctx, args.DeviceID, kinds)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_revoke", func(ctx context.Context, _ *ipc.Call, args struct {
				DeviceID string `json:"deviceId"`
			}) (any, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return nil, err
				}
				if r.config.Grants == nil {
					return nil, errors.New("设备授权未配置")
				}
				return nil, r.config.Grants.Revoke(ctx, args.DeviceID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_rule_list", func(context.Context, *ipc.Call, struct{}) ([]guard.GrantRule, error) {
				if r.config.Grants == nil {
					return nil, errors.New("设备授权未配置")
				}
				return r.config.Grants.Rules(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_rule_set", func(ctx context.Context, _ *ipc.Call, args struct {
				DeviceID  string `json:"deviceId"`
				Action    string `json:"action"`
				Path      string `json:"path"`
				ExpiresAt int64  `json:"expiresAt"`
			}) (guard.GrantRule, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return guard.GrantRule{}, err
				}
				if r.config.Grants == nil {
					return guard.GrantRule{}, errors.New("设备授权未配置")
				}
				var expiresAt time.Time
				if args.ExpiresAt != 0 {
					expiresAt = time.UnixMilli(args.ExpiresAt)
				}
				return r.config.Grants.AddRule(ctx, args.DeviceID, args.Action, args.Path, expiresAt)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_rule_revoke", func(ctx context.Context, _ *ipc.Call, args struct {
				ID string `json:"id"`
			}) (any, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return nil, err
				}
				if r.config.Grants == nil {
					return nil, errors.New("设备授权未配置")
				}
				return nil, r.config.Grants.RevokeRule(ctx, args.ID)
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
