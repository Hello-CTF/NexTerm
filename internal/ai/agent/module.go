package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// guardGrantMutation requires an account or explicit Open identity; target ownership is enforced by Grants.
func (r *Runner) guardGrantMutation(ctx context.Context) error {
	if !r.config.ServerMode {
		return nil
	}
	if _, ok := ipc.UserIDFromContext(ctx); ok || owner.OpenAccess(ctx) {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "AI 请求缺少账号身份")
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

func agentIPCError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	switch {
	case errors.Is(err, ErrJobNotFound):
		return ipc.WrapError(ipc.CodeNotFound, ErrJobNotFound.Error(), err)
	case errors.Is(err, ErrJobForbidden):
		return ipc.WrapError(ipc.CodeForbidden, ErrJobForbidden.Error(), err)
	case errors.Is(err, ErrConfirmationStale):
		return ipc.WrapError(ipc.CodeBadParam, ErrConfirmationStale.Error(), err)
	case errors.Is(err, ErrInvalidConfirmation):
		return ipc.WrapError(ipc.CodeBadParam, ErrInvalidConfirmation.Error(), err)
	case errors.Is(err, ErrSteerQueueFull):
		return ipc.WrapError(ipc.CodeBadParam, ErrSteerQueueFull.Error(), err)
	}
	switch {
	case errors.Is(err, guard.ErrGrantKindsRequired),
		errors.Is(err, guard.ErrInvalidGrantKind),
		errors.Is(err, guard.ErrGrantDeviceRequired),
		errors.Is(err, guard.ErrRuleDeviceRequired),
		errors.Is(err, guard.ErrRuleActionUnsupported),
		errors.Is(err, guard.ErrRulePathRequired),
		errors.Is(err, guard.ErrRulePathUnsupported),
		errors.Is(err, guard.ErrRuleExpiryInvalid),
		errors.Is(err, guard.ErrRuleIDRequired),
		errors.Is(err, guard.ErrDeviceModeInvalid),
		errors.Is(err, guard.ErrDeviceModeDeviceRequired):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	}
	switch err.Error() {
	case "AI 补充指令不能为空",
		"只能编辑用户消息",
		"无法确定设备身份，不能清除设备模式",
		"无法确定设备身份, 权限未保存; 请重新连接后再试",
		"缺少 config":
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	case "消息不存在":
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	case "AI 会话存储未配置", "AI 会话存储不支持消息截断", "权限设置未配置", "设备授权未配置":
		return ipc.WrapError(ipc.CodeUnsupported, err.Error(), err)
	default:
		return err
	}
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
			return ipc.Register(dispatcher, "ai_cancel", func(ctx context.Context, _ *ipc.Call, args struct {
				JobID string `json:"jobId"`
			}) (any, error) {
				return nil, agentIPCError(r.CancelContext(ctx, args.JobID))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_steer", func(ctx context.Context, _ *ipc.Call, args struct {
				JobID   string `json:"jobId"`
				Message string `json:"message"`
			}) (any, error) {
				return nil, agentIPCError(r.SteerContext(ctx, args.JobID, args.Message))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_edit_resend", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
				MessageID      string `json:"messageId"`
			}) (any, error) {
				return nil, agentIPCError(r.EditResend(ctx, args.ConversationID, args.MessageID))
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
				return nil, agentIPCError(r.ConfirmStreamContext(ctx, args, channelStreamFactory(call)))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_answer", func(ctx context.Context, call *ipc.Call, args Answer) (any, error) {
				return nil, agentIPCError(r.AnswerStreamContext(ctx, args, channelStreamFactory(call)))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_run_get", func(ctx context.Context, _ *ipc.Call, args struct {
				JobID string `json:"jobId"`
			}) (RunDTO, error) {
				return r.RunGet(ctx, args.JobID)
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
			return ipc.Register(dispatcher, "ai_hitl_snapshot", func(ctx context.Context, _ *ipc.Call, args struct {
				JobID string `json:"jobId"`
			}) (any, error) {
				return r.HITLSnapshotContext(ctx, args.JobID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_hitl_events", func(ctx context.Context, _ *ipc.Call, args struct {
				JobID    string `json:"jobId"`
				AfterSeq uint64 `json:"afterSeq"`
			}) (any, error) {
				return r.HITLEventsContext(ctx, args.JobID, args.AfterSeq)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_get_permission", func(ctx context.Context, _ *ipc.Call, args struct {
				Scope tools.Scope `json:"scope"`
			}) (guard.Config, error) {
				if r.config.Permissions == nil {
					return guard.Config{}, ipc.NewError(ipc.CodeUnsupported, "权限设置未配置")
				}
				config, err := r.config.Permissions.Get(ctx)
				if err != nil {
					return guard.Config{}, agentIPCError(err)
				}
				config, err = r.config.Permissions.WithDeviceMode(ctx, config, tools.ResolveGrantDeviceID(ctx, r.config.Tools, args.Scope))
				return config, agentIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_set_permission", func(ctx context.Context, _ *ipc.Call, args struct {
				Config       *guard.Config `json:"config"`
				Scope        tools.Scope   `json:"scope"`
				FollowGlobal bool          `json:"followGlobal,omitempty"`
			}) (guard.Config, error) {
				if r.config.Permissions == nil {
					return guard.Config{}, ipc.NewError(ipc.CodeUnsupported, "权限设置未配置")
				}
				deviceID := tools.ResolveGrantDeviceID(ctx, r.config.Tools, args.Scope)
				if args.FollowGlobal {
					if deviceID == "" {
						return guard.Config{}, ipc.NewError(ipc.CodeBadParam, "无法确定设备身份，不能清除设备模式")
					}
					if err := r.config.Permissions.SetDeviceMode(ctx, deviceID, ""); err != nil {
						return guard.Config{}, agentIPCError(err)
					}
					config, err := r.config.Permissions.Get(ctx)
					return config, agentIPCError(err)
				}
				if args.Config == nil {
					return guard.Config{}, ipc.NewError(ipc.CodeBadParam, "缺少 config")
				}
				if deviceID != "" {
					if err := r.config.Permissions.SetDeviceMode(ctx, deviceID, args.Config.Mode); err != nil {
						return guard.Config{}, agentIPCError(err)
					}
					config, err := r.config.Permissions.Get(ctx)
					if err != nil {
						return guard.Config{}, agentIPCError(err)
					}
					config, err = r.config.Permissions.WithDeviceMode(ctx, config, deviceID)
					return config, agentIPCError(err)
				}
				if args.Scope.SessionID != "" || args.Scope.TabID != "" {
					return guard.Config{}, ipc.NewError(ipc.CodeBadParam, "无法确定设备身份, 权限未保存; 请重新连接后再试")
				}
				if err := r.config.Permissions.Set(ctx, *args.Config); err != nil {
					return guard.Config{}, agentIPCError(err)
				}
				config, err := r.config.Permissions.Get(ctx)
				return config, agentIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]guard.DeviceGrant, error) {
				if r.config.Grants == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "设备授权未配置")
				}
				grants, err := r.config.Grants.List(ctx)
				return grants, agentIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_set", func(ctx context.Context, _ *ipc.Call, args struct {
				DeviceID string   `json:"deviceId"`
				Kinds    []string `json:"kinds"`
			}) (guard.DeviceGrant, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return guard.DeviceGrant{}, agentIPCError(err)
				}
				if r.config.Grants == nil {
					return guard.DeviceGrant{}, ipc.NewError(ipc.CodeUnsupported, "设备授权未配置")
				}
				kinds, err := guard.ParseGrantKinds(args.Kinds)
				if err != nil {
					return guard.DeviceGrant{}, agentIPCError(err)
				}
				grant, err := r.config.Grants.Grant(ctx, args.DeviceID, kinds)
				return grant, agentIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_revoke", func(ctx context.Context, _ *ipc.Call, args struct {
				DeviceID string `json:"deviceId"`
			}) (any, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return nil, agentIPCError(err)
				}
				if r.config.Grants == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "设备授权未配置")
				}
				return nil, agentIPCError(r.config.Grants.Revoke(ctx, args.DeviceID))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_rule_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]guard.GrantRule, error) {
				if r.config.Grants == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "设备授权未配置")
				}
				rules, err := r.config.Grants.Rules(ctx)
				return rules, agentIPCError(err)
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
					return guard.GrantRule{}, agentIPCError(err)
				}
				if r.config.Grants == nil {
					return guard.GrantRule{}, ipc.NewError(ipc.CodeUnsupported, "设备授权未配置")
				}
				var expiresAt time.Time
				if args.ExpiresAt != 0 {
					expiresAt = time.UnixMilli(args.ExpiresAt)
				}
				rule, err := r.config.Grants.AddRule(ctx, args.DeviceID, args.Action, args.Path, expiresAt)
				return rule, agentIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_grant_rule_revoke", func(ctx context.Context, _ *ipc.Call, args struct {
				ID string `json:"id"`
			}) (any, error) {
				if err := r.guardGrantMutation(ctx); err != nil {
					return nil, agentIPCError(err)
				}
				if r.config.Grants == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "设备授权未配置")
				}
				return nil, agentIPCError(r.config.Grants.RevokeRule(ctx, args.ID))
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
