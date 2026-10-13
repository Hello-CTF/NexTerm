package production

import (
	"context"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// commandLogSink persists commands observed by the session layer into the command_log table; errors are only logged
// and never interrupt the terminal data plane.
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
