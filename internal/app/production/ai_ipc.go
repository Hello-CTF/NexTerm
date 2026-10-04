package production

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type aiProfileRequest struct {
	Profile profiles.Profile `json:"profile"`
	Preset  string           `json:"preset"`
}

type aiProviderRequest struct {
	Config provider.Config `json:"config"`
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

func registerAICommands(dispatcher *ipc.Dispatcher, manager *profiles.Manager, database *store.Store) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "ai_models", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]string, error) {
				client, err := manager.ActiveClient()
				if err != nil {
					return nil, err
				}
				return client.ListModels(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_test_provider", func(ctx context.Context, _ *ipc.Call, _ struct{}) (provider.TestResult, error) {
				client, err := manager.ActiveClient()
				if err != nil {
					return provider.TestResult{}, err
				}
				return client.Test(ctx), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_get_provider", func(_ context.Context, _ *ipc.Call, _ struct{}) (provider.Config, error) {
				config, ok := manager.ActiveConfig()
				if !ok {
					config = provider.DefaultConfig()
				}
				config.APIKey = profiles.MaskAPIKey(config.APIKey)
				return config, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_set_provider", func(ctx context.Context, _ *ipc.Call, input aiProviderRequest) (any, error) {
				profile, ok := manager.ActiveProfile()
				if !ok {
					profile = profiles.DefaultProfile()
				}
				config := input.Config
				profile.BaseURL = config.BaseURL
				profile.APIKey = config.APIKey
				profile.Model = config.Model
				profile.FallbackModel = config.FallbackModel
				profile.Temperature = config.Temperature
				profile.ContextWindow = config.ContextWindow
				profile.Proxy = config.Proxy
				profile.Stream = config.Stream
				_, err := manager.Save(ctx, profile)
				return nil, err
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
				title := input.Title
				if title == "" {
					title = "新对话"
				}
				row, err := database.ConvCreate(ctx, title, map[string]any{"scope": input.Scope})
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
			return ipc.Register(dispatcher, "ai_model_profiles", func(_ context.Context, _ *ipc.Call, _ struct{}) (profiles.Overview, error) {
				return manager.Overview(), nil
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
					return profiles.Profile{}, err
				}
				for _, saved := range overview.Profiles {
					if saved.ID == profile.ID {
						return saved, nil
					}
				}
				return profiles.Profile{}, profiles.ErrProfileNotFound
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_delete", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) (any, error) {
				_, err := manager.Delete(ctx, input.ID)
				return nil, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_activate", func(ctx context.Context, _ *ipc.Call, input aiConversationRequest) (any, error) {
				_, err := manager.Activate(ctx, input.ID)
				return nil, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_refresh", func(ctx context.Context, _ *ipc.Call, input aiProfileRequest) ([]string, error) {
				config := input.Profile.ProviderConfig()
				if input.Profile.APIKey == profiles.MaskedAPIKey {
					key, err := resolveMaskedProfileKey(ctx, database, input.Profile.ID)
					if err != nil {
						return nil, err
					}
					config.APIKey = key
				}
				client, err := provider.NewClient(config)
				if err != nil {
					return nil, err
				}
				return client.ListModels(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_model_preset", func(_ context.Context, _ *ipc.Call, input aiProfileRequest) (profiles.Profile, error) {
				return profiles.PresetProfile(input.Preset)
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

func resolveMaskedProfileKey(ctx context.Context, database *store.Store, id string) (string, error) {
	raw, found, err := database.SettingGet(ctx, profiles.SettingKey)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%w: %s", profiles.ErrProfileNotFound, id)
	}
	var stored struct {
		Profiles []profiles.Profile `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return "", err
	}
	for _, saved := range stored.Profiles {
		if saved.ID == id {
			return resolveStoredProfileKey(ctx, database, saved.APIKey)
		}
	}
	return "", fmt.Errorf("%w: %s", profiles.ErrProfileNotFound, id)
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
