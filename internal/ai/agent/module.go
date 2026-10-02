package agent

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func Module(runner *Runner) app.Module {
	return app.Module{Name: "ai-agent", RegisterCommands: runner.RegisterCommands, Component: runnerComponent{runner: runner}}
}

func (r *Runner) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "ai_chat", func(ctx context.Context, call *ipc.Call, args ChatArgs) (StartResponse, error) {
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
			return ipc.Register(dispatcher, "ai_conversation_create", func(ctx context.Context, _ *ipc.Call, args struct {
				Title string `json:"title"`
				Scope any    `json:"scope"`
			}) (ConversationDTO, error) {
				return r.CreateConversation(ctx, args.Title, args.Scope)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_conversation_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]ConversationDTO, error) {
				return r.Conversations(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_conversation_delete", func(ctx context.Context, _ *ipc.Call, args struct {
				ID string `json:"id"`
			}) (any, error) {
				return nil, r.DeleteConversation(ctx, args.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_messages", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
			}) ([]MessageDTO, error) {
				return r.Messages(ctx, args.ConversationID)
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
			return ipc.Register(dispatcher, "ai_set_permission", func(ctx context.Context, _ *ipc.Call, config guard.Config) (guard.Config, error) {
				if r.config.Permissions == nil {
					return guard.Config{}, errors.New("权限设置未配置")
				}
				if err := r.config.Permissions.Set(ctx, config); err != nil {
					return guard.Config{}, err
				}
				return r.config.Permissions.Get(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_presets", func(context.Context, *ipc.Call, struct{}) ([]provider.Preset, error) {
				return provider.Presets(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_models", r.models)
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_refresh", r.models)
		},
		func() error {
			return ipc.Register(dispatcher, "ai_test_provider", func(ctx context.Context, _ *ipc.Call, config provider.Config) (provider.TestResult, error) {
				client, err := r.clientForConfig(config)
				if err != nil {
					return provider.TestResult{}, err
				}
				return client.Test(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_get_provider", func(context.Context, *ipc.Call, struct{}) (provider.Config, error) {
				if r.config.Profiles == nil {
					return provider.Config{}, errors.New("模型配置未配置")
				}
				config, ok := r.config.Profiles.ActiveConfig()
				if !ok {
					return provider.Config{}, profiles.ErrNoActiveProfile
				}
				return config, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_set_provider", func(ctx context.Context, _ *ipc.Call, config provider.Config) (profiles.Overview, error) {
				if r.config.Profiles == nil {
					return profiles.Overview{}, errors.New("模型配置未配置")
				}
				profile, _ := r.config.Profiles.ActiveProfile()
				profile.Name = config.Model
				profile.BaseURL, profile.APIKey, profile.Model, profile.FallbackModel = config.BaseURL, config.APIKey, config.Model, config.FallbackModel
				profile.Temperature, profile.ContextWindow, profile.Proxy, profile.Stream = config.Temperature, config.ContextWindow, config.Proxy, config.Stream
				return r.config.Profiles.Save(ctx, profile)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_profiles", func(context.Context, *ipc.Call, struct{}) (profiles.Overview, error) {
				if r.config.Profiles == nil {
					return profiles.Overview{}, errors.New("模型配置未配置")
				}
				return r.config.Profiles.Overview(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_save", func(ctx context.Context, _ *ipc.Call, args struct {
				Profile profiles.Profile `json:"profile"`
			}) (profiles.Overview, error) {
				if r.config.Profiles == nil {
					return profiles.Overview{}, errors.New("模型配置未配置")
				}
				return r.config.Profiles.Save(ctx, args.Profile)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_delete", func(ctx context.Context, _ *ipc.Call, args struct {
				ID string `json:"id"`
			}) (profiles.Overview, error) {
				if r.config.Profiles == nil {
					return profiles.Overview{}, errors.New("模型配置未配置")
				}
				return r.config.Profiles.Delete(ctx, args.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_activate", func(ctx context.Context, _ *ipc.Call, args struct {
				ID string `json:"id"`
			}) (profiles.Overview, error) {
				if r.config.Profiles == nil {
					return profiles.Overview{}, errors.New("模型配置未配置")
				}
				return r.config.Profiles.Activate(ctx, args.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_preset", func(_ context.Context, _ *ipc.Call, args struct {
				Preset string `json:"preset"`
			}) (profiles.Profile, error) {
				return profiles.PresetProfile(args.Preset)
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

func (r *Runner) models(ctx context.Context, _ *ipc.Call, _ struct{}) ([]string, error) {
	if r.config.Profiles == nil {
		return nil, errors.New("模型配置未配置")
	}
	client, err := r.config.Profiles.ActiveClient()
	if err != nil {
		return nil, err
	}
	return client.ListModels(ctx)
}

func (r *Runner) clientForConfig(config provider.Config) (*provider.Client, error) {
	if config.BaseURL == "" && config.Model == "" && r.config.Profiles != nil {
		return r.config.Profiles.ActiveClient()
	}
	return provider.NewClient(config)
}
