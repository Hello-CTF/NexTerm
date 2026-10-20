package production

import (
	"context"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func migrateProductionOwnerlessAI(ctx context.Context, database *store.Store, logger *slog.Logger, shared bool) error {
	report, err := cron.MigrateOwnerlessAI(ctx, database.GORM(), shared)
	if err != nil {
		return err
	}
	if !report.Changed() && (shared || report.Unattributed == 0) {
		return nil
	}
	values := []any{
		"cron_assigned", report.CronJobsAssigned,
		"cron_quarantined", report.CronJobsQuarantined,
		"runs_assigned", report.RunsAssigned,
		"messages_assigned", report.MessagesAssigned,
		"run_events_assigned", report.RunEventsAssigned,
		"hitl_runs_assigned", report.HitlRunsAssigned,
		"checkpoints_assigned", report.CheckpointsAssigned,
		"unattributed", report.Unattributed,
	}
	if !shared && report.Unattributed > 0 {
		logger.Warn("ownerless AI history remains quarantined", values...)
	} else {
		logger.Info("migrated ownerless AI history", values...)
	}
	return nil
}
