package production

import (
	"context"
	"log/slog"

	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// commandLogSink 把会话层观察到的命令落入 command_log 表; 错误只记日志,
// 不打断终端数据面。
type commandLogSink struct {
	database *store.Store
	logger   *slog.Logger
}

func (s commandLogSink) RecordCommand(ctx context.Context, record session.CommandRecord) {
	err := s.database.CommandLogInsert(ctx, store.CommandLogInput{
		SessionID:  record.SessionID,
		TabID:      record.TabID,
		AssetID:    record.AssetID,
		UserID:     record.UserID,
		Command:    record.Command,
		Source:     record.Source,
		ExitCode:   record.ExitCode,
		StartedAt:  record.StartedAt.UnixMilli(),
		FinishedAt: record.FinishedAt.UnixMilli(),
	})
	if err != nil && s.logger != nil {
		s.logger.Warn("command log insert failed", "session", record.SessionID, "tab", record.TabID, "error", err)
	}
}
