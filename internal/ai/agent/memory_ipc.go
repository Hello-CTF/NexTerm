package agent

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	memoryCommandCreate      = "memory_create"
	memoryCommandGet         = "memory_get"
	memoryCommandEdit        = "memory_edit"
	memoryCommandDelete      = "memory_delete"
	memoryCommandIndex       = "memory_index"
	memoryCommandSettingsGet = "memory_settings_get"
	memoryCommandSettingsSet = "memory_settings_set"
)

type memoryScopeDTO struct {
	Tenant  string `json:"tenant"`
	Subject string `json:"subject"`
}

type memoryEntryDTO struct {
	ID        string `json:"id"`
	Topic     string `json:"topic"`
	Content   string `json:"content"`
	Version   uint64 `json:"version"`
	Redacted  bool   `json:"redacted"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type memoryIndexEntryDTO struct {
	ID        string `json:"id"`
	Version   uint64 `json:"version"`
	Redacted  bool   `json:"redacted"`
	UpdatedAt int64  `json:"updatedAt"`
}

type memoryTopicIndexDTO struct {
	Topic   string                `json:"topic"`
	Entries []memoryIndexEntryDTO `json:"entries"`
}

type memorySettingsDTO struct {
	InjectionEnabled bool   `json:"injectionEnabled"`
	ToolsEnabled     bool   `json:"toolsEnabled"`
	Version          uint64 `json:"version"`
}

type memoryCreateRequest struct {
	Scope   memoryScopeDTO `json:"scope"`
	Topic   string         `json:"topic"`
	Content string         `json:"content"`

	Secrets string `json:"secrets,omitempty"`
}

type memoryGetRequest struct {
	Scope memoryScopeDTO `json:"scope"`
	ID    string         `json:"id"`
}

type memoryEditRequest struct {
	Scope           memoryScopeDTO `json:"scope"`
	ID              string         `json:"id"`
	ExpectedVersion uint64         `json:"expectedVersion"`
	Topic           *string        `json:"topic,omitempty"`
	Content         *string        `json:"content,omitempty"`
	Secrets         string         `json:"secrets,omitempty"`
}

type memoryDeleteRequest struct {
	Scope           memoryScopeDTO `json:"scope"`
	ID              string         `json:"id"`
	ExpectedVersion uint64         `json:"expectedVersion"`
}

type memoryIndexRequest struct {
	Scope memoryScopeDTO `json:"scope"`
}

type memorySettingsGetRequest struct {
	Scope memoryScopeDTO `json:"scope"`
}

type memorySettingsSetRequest struct {
	Scope            memoryScopeDTO `json:"scope"`
	ExpectedVersion  uint64         `json:"expectedVersion"`
	InjectionEnabled *bool          `json:"injectionEnabled,omitempty"`
	ToolsEnabled     *bool          `json:"toolsEnabled,omitempty"`
}

func memoryEntry(entry memory.Entry) memoryEntryDTO {
	return memoryEntryDTO{
		ID: entry.ID, Topic: entry.Topic, Content: entry.Content, Version: entry.Version,
		Redacted: entry.Redacted, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
	}
}

func memorySettings(settings memory.Settings) memorySettingsDTO {
	return memorySettingsDTO{
		InjectionEnabled: settings.InjectionEnabled, ToolsEnabled: settings.ToolsEnabled, Version: settings.Version,
	}
}

func memorySecretPolicy(name string) (memory.SecretPolicy, error) {
	switch name {
	case "", "reject":
		return memory.SecretReject, nil
	case "redact":
		return memory.SecretRedact, nil
	default:
		return 0, ipc.BadParam(errors.New("secrets 必须是 reject 或 redact"))
	}
}

func memoryIPCError(err error) *ipc.Error {
	var conflict *memory.VersionConflictError
	switch {
	case errors.As(err, &conflict):
		return ipc.NewError(ipc.CodeBadParam, conflict.Error()).WithDetail(map[string]any{
			"id": conflict.ID, "expected": conflict.Expected, "actual": conflict.Actual,
		})
	case errors.Is(err, memory.ErrNotFound):
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	case errors.Is(err, memory.ErrPermissionDenied):
		return ipc.WrapError(ipc.CodeForbidden, err.Error(), err)
	case errors.Is(err, memory.ErrSensitiveContent):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	case errors.Is(err, memory.ErrInvalidInput):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	default:
		return ipc.NormalizeError(err)
	}
}

func (r *Runner) registerMemoryCommands(dispatcher *ipc.Dispatcher) error {
	store := r.config.Memory
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, memoryCommandCreate, func(ctx context.Context, _ *ipc.Call, input memoryCreateRequest) (memoryEntryDTO, error) {
				policy, err := memorySecretPolicy(input.Secrets)
				if err != nil {
					return memoryEntryDTO{}, err
				}
				entry, err := store.Create(ctx, memory.Scope(input.Scope), memory.CreateInput{Topic: input.Topic, Content: input.Content, Secrets: policy})
				if err != nil {
					return memoryEntryDTO{}, memoryIPCError(err)
				}
				return memoryEntry(entry), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, memoryCommandGet, func(ctx context.Context, _ *ipc.Call, input memoryGetRequest) (memoryEntryDTO, error) {
				entry, err := store.Get(ctx, memory.Scope(input.Scope), input.ID)
				if err != nil {
					return memoryEntryDTO{}, memoryIPCError(err)
				}
				return memoryEntry(entry), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, memoryCommandEdit, func(ctx context.Context, _ *ipc.Call, input memoryEditRequest) (memoryEntryDTO, error) {
				policy, err := memorySecretPolicy(input.Secrets)
				if err != nil {
					return memoryEntryDTO{}, err
				}
				entry, err := store.Edit(ctx, memory.Scope(input.Scope), input.ID, input.ExpectedVersion, memory.EditInput{
					Topic: input.Topic, Content: input.Content, Secrets: policy,
				})
				if err != nil {
					return memoryEntryDTO{}, memoryIPCError(err)
				}
				return memoryEntry(entry), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, memoryCommandDelete, func(ctx context.Context, _ *ipc.Call, input memoryDeleteRequest) (any, error) {
				if err := store.Delete(ctx, memory.Scope(input.Scope), input.ID, input.ExpectedVersion); err != nil {
					return nil, memoryIPCError(err)
				}
				return nil, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, memoryCommandIndex, func(ctx context.Context, _ *ipc.Call, input memoryIndexRequest) ([]memoryTopicIndexDTO, error) {
				index, err := store.Index(ctx, memory.Scope(input.Scope))
				if err != nil {
					return nil, memoryIPCError(err)
				}
				result := make([]memoryTopicIndexDTO, len(index))
				for topicIndex, topic := range index {
					entries := make([]memoryIndexEntryDTO, len(topic.Entries))
					for entryIndex, entry := range topic.Entries {
						entries[entryIndex] = memoryIndexEntryDTO(entry)
					}
					result[topicIndex] = memoryTopicIndexDTO{Topic: topic.Topic, Entries: entries}
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, memoryCommandSettingsGet, func(ctx context.Context, _ *ipc.Call, input memorySettingsGetRequest) (memorySettingsDTO, error) {
				settings, err := store.Settings(ctx, memory.Scope(input.Scope))
				if err != nil {
					return memorySettingsDTO{}, memoryIPCError(err)
				}
				return memorySettings(settings), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, memoryCommandSettingsSet, func(ctx context.Context, _ *ipc.Call, input memorySettingsSetRequest) (memorySettingsDTO, error) {
				settings, err := store.UpdateSettings(ctx, memory.Scope(input.Scope), memory.SettingsInput{
					InjectionEnabled: input.InjectionEnabled, ToolsEnabled: input.ToolsEnabled,
				}, input.ExpectedVersion)
				if err != nil {
					return memorySettingsDTO{}, memoryIPCError(err)
				}
				return memorySettings(settings), nil
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
