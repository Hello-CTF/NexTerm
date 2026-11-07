package production

import (
	"context"
	"fmt"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type transcriptStarRequest struct {
	ID      string `json:"id"`
	Starred bool   `json:"starred"`
}

type transcriptCutoffRequest struct {
	// EndedBefore is a Unix time in milliseconds; transcripts that ended before it are counted or deleted.
	EndedBefore int64 `json:"endedBefore"`
}

type transcriptUsageDTO struct {
	Total          int64 `json:"total"`
	Starred        int64 `json:"starred"`
	StoredBytes    int64 `json:"storedBytes"`
	Purgeable      int64 `json:"purgeable"`
	PurgeableBytes int64 `json:"purgeableBytes"`
}

type transcriptPurgeResult struct {
	Deleted int64 `json:"deleted"`
}

type transcriptRetentionDTO struct {
	Enabled        bool  `json:"enabled"`
	MaxAgeMS       int64 `json:"maxAgeMs"`
	MaxCount       int64 `json:"maxCount"`
	UnlimitedCount bool  `json:"unlimitedCount"`
	// Editable is false on the multi-user server, where automatic cleanup is one server-wide policy.
	Editable bool `json:"editable"`
}

type transcriptRetentionRequest struct {
	Enabled        bool  `json:"enabled"`
	MaxAgeMS       int64 `json:"maxAgeMs"`
	MaxCount       int64 `json:"maxCount"`
	UnlimitedCount bool  `json:"unlimitedCount"`
}

func (s *terminalCommandService) registerTranscriptCleanup(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "transcript_star", func(ctx context.Context, _ *ipc.Call, input transcriptStarRequest) (any, error) {
		if err := store.EnsureID(input.ID); err != nil {
			return nil, err
		}
		return nil, s.database.TranscriptSetStarred(ctx, input.ID, input.Starred)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "transcript_usage", func(ctx context.Context, _ *ipc.Call, input transcriptCutoffRequest) (transcriptUsageDTO, error) {
		usage, err := s.database.TranscriptUsageBefore(ctx, input.EndedBefore)
		if err != nil {
			return transcriptUsageDTO{}, err
		}
		return transcriptUsageDTO{
			Total: usage.Total, Starred: usage.Starred, StoredBytes: usage.StoredBytes,
			Purgeable: usage.Purgeable, PurgeableBytes: usage.PurgeableBytes,
		}, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "transcript_purge", func(ctx context.Context, _ *ipc.Call, input transcriptCutoffRequest) (transcriptPurgeResult, error) {
		if input.EndedBefore <= 0 {
			return transcriptPurgeResult{}, ipc.BadParam(fmt.Errorf("清理时间点无效"))
		}
		deleted, err := s.database.TranscriptPurgeEndedBefore(ctx, input.EndedBefore)
		return transcriptPurgeResult{Deleted: deleted}, err
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "transcript_retention_get", func(ctx context.Context, _ *ipc.Call, _ struct{}) (transcriptRetentionDTO, error) {
		settings, err := core.ReadTranscriptRetentionSettings(ctx, s.database)
		if err != nil {
			return transcriptRetentionDTO{}, err
		}
		result := transcriptRetentionDTO{
			Enabled:        settings.Enabled == nil || *settings.Enabled,
			MaxAgeMS:       settings.MaxAgeMS,
			MaxCount:       settings.MaxCount,
			UnlimitedCount: settings.UnlimitedCount,
			Editable:       ipc.OwnerID(ctx) == "",
		}
		if result.MaxAgeMS == 0 {
			result.MaxAgeMS = core.TranscriptDefaultRetentionMaxAge.Milliseconds()
		}
		if result.MaxCount == 0 {
			result.MaxCount = core.TranscriptDefaultRetentionMaxCount
		}
		return result, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "transcript_retention_set", func(ctx context.Context, _ *ipc.Call, input transcriptRetentionRequest) (any, error) {
		if ipc.OwnerID(ctx) != "" {
			return nil, ipc.NewError(ipc.CodeUnsupported, "多用户服务端的自动清理由服务端统一设置")
		}
		if input.MaxAgeMS <= 0 || (!input.UnlimitedCount && input.MaxCount <= 0) {
			return nil, ipc.BadParam(fmt.Errorf("保留时长和数量必须大于 0"))
		}
		enabled := input.Enabled
		return nil, core.WriteTranscriptRetentionSettings(ctx, s.database, core.TranscriptRetentionSettings{
			Enabled: &enabled, MaxAgeMS: input.MaxAgeMS, MaxCount: input.MaxCount, UnlimitedCount: input.UnlimitedCount,
		})
	})
}
