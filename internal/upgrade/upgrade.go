package upgrade

import (
	"context"
	"errors"
	"fmt"

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
	AI                         AIHistoryReport
	AISettings                 AISettingsReport
}

// Migrate attributes ownerless data to the first active superadmin when an
// account-mode server starts on a database last used in Open mode (or before
// accounts existed): owned rows, AI history keyed by conversation owner, and a
// copy of the global AI settings. Shared desktop/Open mode keeps ownerless data
// and global settings in their original scope. A deferred report is successful
// and safe to retry after an active superadmin is created.
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
				if err := migrateAIHistory(tx, &report.AI); err != nil {
					return err
				}
				if err := migrateAISettings(tx, ownerID, &report.AISettings); err != nil {
					return err
				}
			}
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
		aiHistoryMigrationPending,
		aiSettingsMigrationPending,
	} {
		found, err := pending(database)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}
