package upgrade

import (
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

const aiPermissionTombstoneKind = "ai_permission"

var aiOwnedTombstoneKinds = []string{
	store.SyncTombstoneKindAIProfile,
	aiPermissionTombstoneKind,
	store.SyncTombstoneKindAIGrant,
}

func syncMigrationPending(database *gorm.DB) (bool, error) {
	return hasOwnerlessRows(database.Model(&store.SyncTombstone{}).
		Where("kind NOT IN ?", aiOwnedTombstoneKinds))
}

func migrateSync(database *gorm.DB, ownerID string, report *OwnershipReport) error {
	return migrateCompositeOwnership(database, compositeOwnershipTable{
		model: &store.SyncTombstone{}, idColumn: "id",
		sourceWhere: "kind NOT IN ?", sourceArgs: []any{aiOwnedTombstoneKinds},
	}, ownerID, report)
}
