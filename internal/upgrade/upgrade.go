package upgrade

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner/legacy"
	"gorm.io/gorm"
)

type OwnershipReport struct {
	Assigned  int64
	Conflicts int64
}

type LayoutReport struct {
	Copied    int64
	Existing  int64
	Conflicts int64
}

type Report struct {
	OwnerID                    string
	DeferredNoActiveSuperadmin bool
	Ownership                  OwnershipReport
	Layout                     LayoutReport
	AI                         cron.OwnerlessMigrationReport
	AISettings                 legacy.Report
}

// Migrate upgrades GORM-era data before the existing AI migrations run. Shared
// desktop/Open mode keeps ownerless data and global settings in their original
// scope. A deferred report is successful and safe to retry after an active
// superadmin is created.
func Migrate(ctx context.Context, database *gorm.DB, shared bool) (Report, error) {
	if database == nil {
		return Report{}, errors.New("data upgrade: nil database")
	}
	var report Report
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !shared {
			pending, err := dataMigrationPending(tx)
			if err != nil {
				return err
			}
			if pending {
				ownerID, err := firstActiveSuperadmin(tx)
				if err != nil {
					return err
				}
				if ownerID == "" {
					report.DeferredNoActiveSuperadmin = true
					return nil
				}
				report.OwnerID = ownerID
				if err := migrateOwnership(tx, ownerID, &report.Ownership); err != nil {
					return err
				}
				if err := migrateVault(tx, ownerID, &report.Ownership); err != nil {
					return err
				}
				if err := migrateSync(tx, ownerID, &report.Ownership); err != nil {
					return err
				}
				if err := migrateLayout(tx, ownerID, &report.Layout); err != nil {
					return err
				}
			}
		}

		aiReport, err := cron.MigrateOwnerlessAI(ctx, tx, shared)
		if err != nil {
			return err
		}
		report.AI = aiReport
		aiSettingsReport, err := legacy.Migrate(ctx, tx, shared)
		if err != nil {
			return err
		}
		report.AISettings = aiSettingsReport
		if aiSettingsReport.SkippedNoActiveSuperadmin {
			report.DeferredNoActiveSuperadmin = true
		}
		return nil
	})
	if err != nil {
		return Report{}, fmt.Errorf("data upgrade: %w", err)
	}
	return report, nil
}

func dataMigrationPending(database *gorm.DB) (bool, error) {
	for _, pending := range []func(*gorm.DB) (bool, error){
		ownershipMigrationPending,
		vaultMigrationPending,
		syncMigrationPending,
		layoutMigrationPending,
	} {
		found, err := pending(database)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}
