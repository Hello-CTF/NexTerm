package production

import (
	"context"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner/legacy"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func migrateProductionLegacyAISettings(ctx context.Context, database *store.Store, logger *slog.Logger, shared bool) error {
	report, err := legacy.Migrate(ctx, database.GORM(), shared)
	if err != nil {
		return err
	}
	if report.SkippedNoActiveSuperadmin {
		logger.Warn("legacy AI settings migration deferred", "skipped_no_active_superadmin", 1)
		return nil
	}
	total := report.Total()
	if total == (legacy.ItemReport{}) {
		return nil
	}
	values := []any{
		"migrated", total.Migrated,
		"existing", total.Existing,
		"conflicts", total.Conflicts,
		"tombstoned", total.Tombstoned,
		"ledger_entries", report.LedgerEntries,
	}
	if total.Conflicts > 0 || total.Tombstoned > 0 {
		logger.Warn("legacy AI settings migration completed with preserved entries", values...)
	} else {
		logger.Info("migrated legacy AI settings", values...)
	}
	return nil
}
