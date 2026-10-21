package upgrade

import (
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func vaultMigrationPending(database *gorm.DB) (bool, error) {
	for _, model := range []any{&store.CredentialRow{}, &store.CredentialTombstone{}} {
		found, err := hasOwnerlessRows(database.Model(model))
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

func migrateVault(database *gorm.DB, ownerID string, report *OwnershipReport) error {
	assigned, err := assignOwnerless(database.Model(&store.CredentialRow{}), ownerID)
	if err != nil {
		return err
	}
	report.Assigned += assigned
	return migrateCompositeOwnership(database, compositeOwnershipTable{
		model: &store.CredentialTombstone{}, idColumn: "id",
	}, ownerID, report)
}
