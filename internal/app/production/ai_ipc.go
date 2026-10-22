package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type aiProfileRequest struct {
	Profile profiles.Profile `json:"profile"`
	Preset  string           `json:"preset"`
}

type aiProviderRequest struct {
	Config provider.Config `json:"config"`
}

type aiTestProviderRequest struct {
	ID string `json:"id"`
}

type aiCircuitStatusRequest struct {
	ID string `json:"id"`
}

type aiCircuitStatusDTO struct {
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	OpenUntil           *int64 `json:"openUntil"`
}

type aiConversationRequest struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	ConversationID string `json:"conversationId"`
	Scope          any    `json:"scope"`
}

type conversationDTO struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Scope     json.RawMessage `json:"scope"`
	CreatedAt int64           `json:"createdAt"`
	UpdatedAt int64           `json:"updatedAt"`
}

type messageDTO struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	Role           string          `json:"role"`
	Content        json.RawMessage `json:"content"`
	TokensIn       *int64          `json:"tokensIn"`
	TokensOut      *int64          `json:"tokensOut"`
	CreatedAt      int64           `json:"createdAt"`
}

func registerAICommands(dispatcher *ipc.Dispatcher, manager *profiles.Manager, database *store.Store, validators ...agent.ScopeValidator) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "ai_models", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]string, error) {
				client, err := manager.ActiveClient(ctx)
				if err != nil {
					return nil, aiIPCError(err)
				}
				return client.ListModels(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_test_provider", func(ctx context.Context, _ *ipc.Call, input aiTestProviderRequest) (provider.TestResult, error) {
				if input.ID != "" {
					client, err := manager.ClientFor(ctx, input.ID)
					if err != nil {
						return provider.TestResult{}, aiIPCError(err)
					}
					return client.Test(ctx), nil
				}
				client, err := manager.ActiveClient(ctx)
				if err != nil {
					return provider.TestResult{}, aiIPCError(err)
				}
				return client.Test(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_get_provider", func(ctx context.Context, _ *ipc.Call, _ struct{}) (provider.Config, error) {
				config, ok, err := manager.ActiveConfig(ctx)
				if err != nil {
					return provider.Config{}, aiIPCError(err)
				}
				if !ok {
					config = provider.DefaultConfig()
				}
				config.APIKey = profiles.MaskAPIKey(config.APIKey)
				return config, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_set_provider", func(ctx context.Context, _ *ipc.Call, input aiProviderRequest) (any, error) {
				profile, ok, err := manager.ActiveProfile(ctx)
				if err != nil {
					return nil, aiIPCError(err)
				}
				if !ok {
					profile = profiles.DefaultProfile()
				}
				config := input.Config
				profile.BaseURL = config.BaseURL
				profile.APIKey = config.APIKey
				profile.Model = config.Model
				profile.FallbackModel = config.FallbackModel
				profile.Temperature = config.Temperature
				profile.ReasoningEffort = config.ReasoningEffort
				profile.ContextWindow = config.ContextWindow
				profile.Proxy = config.Proxy
				profile.Stream = config.Stream
				_, err = manager.Save(ctx, profile)
				return nil, aiIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_presets", func(_ context.Context, _ *ipc.Call, _ struct{}) ([]string, error) {
				presets := provider.Presets()
				result := make([]string, len(presets))
				for index, preset := range presets {
					result[index] = preset.ID
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_conversation_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]conversationDTO, error) {
				rows, err := database.ConvList(ctx)
				result := make([]conversationDTO, len(rows))
				for index, row := range rows {
					result[index] = productionConversation(row)
				}
				return result, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_conversation_delete", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) (any, error) {
				return nil, database.ConvDelete(ctx, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_conversation_create", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) (conversationDTO, error) {
				scopeValue := input.Scope
				if len(validators) != 0 && validators[0] != nil {
					scope, err := scopeFromConversationValue(scopeValue)
					if err != nil {
						return conversationDTO{}, aiIPCError(err)
					}
					if resolver, ok := validators[0].(agent.ScopeResolver); ok {
						resolved, err := resolver.ResolveScope(ctx, "", scope)
						if err != nil {
							return conversationDTO{}, aiIPCError(err)
						}
						if scopeValue != nil {
							scopeValue = resolved
						}
					} else if err := validators[0].ValidateScope(ctx, scope); err != nil {
						return conversationDTO{}, aiIPCError(err)
					}
				}
				title := input.Title
				if title == "" {
					title = "新对话"
				}
				row, err := database.ConvCreate(ctx, title, map[string]any{"scope": scopeValue})
				return productionConversation(row), err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_messages", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) ([]messageDTO, error) {
				rows, err := database.MsgList(ctx, input.ConversationID)
				result := make([]messageDTO, len(rows))
				for index, row := range rows {
					result[index] = messageDTO{
						ID: row.ID, ConversationID: row.ConversationID, Role: row.Role, Content: store.ParseJSONOr(row.ContentJSON),
						TokensIn: row.TokensIn, TokensOut: row.TokensOut, CreatedAt: row.CreatedAt,
					}
				}
				return result, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_profiles", func(ctx context.Context, _ *ipc.Call, _ struct{}) (profiles.Overview, error) {
				overview, err := manager.Overview(ctx)
				return overview, aiIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_circuit_status", func(ctx context.Context, _ *ipc.Call, input aiCircuitStatusRequest) (aiCircuitStatusDTO, error) {
				id := input.ID
				if id == "" {
					profile, ok, err := manager.ActiveProfile(ctx)
					if err != nil {
						return aiCircuitStatusDTO{}, aiIPCError(err)
					}
					if !ok {
						return aiCircuitStatusDTO{}, aiIPCError(profiles.ErrNoActiveProfile)
					}
					id = profile.ID
				} else {
					_, ok, err := manager.Profile(ctx, id)
					if err != nil {
						return aiCircuitStatusDTO{}, aiIPCError(err)
					}
					if !ok {
						return aiCircuitStatusDTO{}, aiIPCError(fmt.Errorf("%w: %s", profiles.ErrProfileNotFound, id))
					}
				}
				status, ok, err := manager.CircuitStatus(ctx, id)
				if err != nil {
					return aiCircuitStatusDTO{}, aiIPCError(err)
				}
				if !ok {
					return aiCircuitStatusDTO{}, nil
				}
				dto := aiCircuitStatusDTO{ConsecutiveFailures: status.ConsecutiveFailures}
				if status.OpenUntil != nil {
					openUntil := status.OpenUntil.UnixMilli()
					dto.OpenUntil = &openUntil
				}
				return dto, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_save", func(ctx context.Context, _ *ipc.Call, input aiProfileRequest) (profiles.Profile, error) {
				profile := input.Profile
				if profile.ID == "" {
					profile.ID = ids.New()
				}
				profile = profile.Normalized()
				overview, err := manager.Save(ctx, profile)
				if err != nil {
					return profiles.Profile{}, aiIPCError(err)
				}
				for _, saved := range overview.Profiles {
					if saved.ID == profile.ID {
						return saved, nil
					}
				}
				return profiles.Profile{}, aiIPCError(profiles.ErrProfileNotFound)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_delete", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) (any, error) {
				_, err := manager.Delete(ctx, input.ID)
				return nil, aiIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_activate", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) (any, error) {
				_, err := manager.Activate(ctx, input.ID)
				return nil, aiIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_refresh", func(ctx context.Context, _ *ipc.Call, input aiProfileRequest) (provider.ModelList, error) {
				// ActiveProfile applies the same owner resolver as every other Manager operation.
				// In particular, a server gateway with no account cannot reach the plaintext-key provider branch.
				if _, _, err := manager.ActiveProfile(ctx); err != nil {
					return provider.ModelList{}, aiIPCError(err)
				}
				config := input.Profile.ProviderConfig()
				if strings.TrimSpace(config.APIKey) == profiles.MaskedAPIKey {
					key, err := resolveMaskedProfileKey(ctx, manager, database, input.Profile.ID)
					if err != nil {
						return provider.ModelList{}, aiIPCError(err)
					}
					if err := profiles.RejectMaskedKey(key); err != nil {
						return provider.ModelList{}, aiIPCError(err)
					}
					config.APIKey = key
				}
				client, err := provider.NewClient(config, input.Profile.ClientOptions()...)
				if err != nil {
					return provider.ModelList{}, aiIPCError(err)
				}
				return client.ListModelsDetailed(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_preset", func(_ context.Context, _ *ipc.Call, input aiProfileRequest) (profiles.Profile, error) {
				return profiles.PresetProfile(input.Preset)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_usage_summary", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]store.RunUsageSummaryRow, error) {
				return database.RunUsageSummary(ctx)
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

func aiIPCError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	switch {
	case errors.Is(err, profiles.ErrProfileNotFound):
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	case errors.Is(err, profiles.ErrNoActiveProfile), errors.Is(err, profiles.ErrProfileKeyUnavailable):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	default:
		return err
	}
}

func resolveMaskedProfileKey(ctx context.Context, manager *profiles.Manager, database *store.Store, id string) (string, error) {
	if err := manager.Reload(ctx); err != nil {
		return "", err
	}
	stored, found, err := manager.Profile(ctx, id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%w: %s", profiles.ErrProfileNotFound, id)
	}
	return resolveStoredProfileKey(ctx, database, stored.APIKey)
}

func resolveStoredProfileKey(ctx context.Context, database *store.Store, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	protector := database.SecretProtector()
	if protector == nil {
		return stored, nil
	}
	if strings.HasPrefix(stored, store.SecretEnvelopePrefix) {
		plaintext, err := protector.DecryptSecret(ctx, stored)
		if err != nil {
			return "", err
		}
		return plaintext, nil
	}
	if _, err := protector.EncryptSecret(ctx, stored); err != nil {
		return "", ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return stored, nil
}

func productionConversation(row store.ConversationRow) conversationDTO {
	return conversationDTO{
		ID: row.ID, Title: row.Title, Scope: store.ParseJSONOr(row.ScopeJSON), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
