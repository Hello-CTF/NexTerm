package upgrade

import (
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

var directOwnershipModels = []any{
	&store.AssetGroupRow{},
	&store.AssetRow{},
	&store.SnippetRow{},
	&store.ConversationRow{},
	&store.TranscriptRow{},
	&store.TranscriptChunkRow{},
	&store.DurableTranscriptOffset{},
	&store.DurableTabTitle{},
	&store.RecordingRow{},
	&store.AuditRow{},
}

func firstActiveSuperadmin(database *gorm.DB) (string, error) {
	var user store.AppUser
	err := database.Select("id").
		Where("role = ? AND state = ?", "superadmin", "active").
		Order("created_at").Order("id").
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return user.ID, nil
}

func ownershipMigrationPending(database *gorm.DB) (bool, error) {
	for _, model := range directOwnershipModels {
		found, err := hasOwnerlessRows(database.Model(model))
		if err != nil || found {
			return found, err
		}
	}
	for _, model := range []any{&store.KnownHostRow{}, &store.OutcomeRecord{}} {
		found, err := hasOwnerlessRows(database.Model(model))
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

func hasOwnerlessRows(query *gorm.DB) (bool, error) {
	var found int
	result := query.Select("1").Where("owner_id = ''").Limit(1).Scan(&found)
	return found == 1, result.Error
}

func migrateOwnership(database *gorm.DB, ownerID string, report *OwnershipReport) error {
	for _, model := range directOwnershipModels {
		assigned, err := assignOwnerless(database.Model(model), ownerID)
		if err != nil {
			return err
		}
		report.Assigned += assigned
	}
	if err := migrateKnownHosts(database, ownerID, report); err != nil {
		return err
	}
	return migrateCompositeOwnership(database, compositeOwnershipTable{
		model: &store.OutcomeRecord{}, idColumn: "idempotence_key",
	}, ownerID, report)
}

func assignOwnerless(query *gorm.DB, ownerID string) (int64, error) {
	result := query.Where("owner_id = ''").UpdateColumn("owner_id", ownerID)
	return result.RowsAffected, result.Error
}

type compositeOwnershipTable struct {
	model       any
	idColumn    string
	sourceWhere string
	sourceArgs  []any
}

func migrateCompositeOwnership(database *gorm.DB, table compositeOwnershipTable, ownerID string, report *OwnershipReport) error {
	query := database.Model(table.model).Where("owner_id = ''")
	if table.sourceWhere != "" {
		query = query.Where(table.sourceWhere, table.sourceArgs...)
	}
	var ids []string
	if err := query.Select(table.idColumn).Order(table.idColumn).Scan(&ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		exists, err := compositeTargetExists(database.Model(table.model), table.idColumn, id, ownerID)
		if err != nil {
			return err
		}
		if exists {
			report.Conflicts++
			continue
		}
		result := database.Model(table.model).
			Where(table.idColumn+" = ? AND owner_id = ''", id).
			UpdateColumn("owner_id", ownerID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			report.Assigned++
			continue
		}
		exists, err = compositeTargetExists(database.Model(table.model), table.idColumn, id, ownerID)
		if err != nil {
			return err
		}
		if exists {
			report.Conflicts++
		}
	}
	return nil
}

func compositeTargetExists(query *gorm.DB, idColumn, id, ownerID string) (bool, error) {
	var found int
	result := query.Select("1").Where(idColumn+" = ? AND owner_id = ?", id, ownerID).Limit(1).Scan(&found)
	return found == 1, result.Error
}

func migrateKnownHosts(database *gorm.DB, ownerID string, report *OwnershipReport) error {
	var rows []store.KnownHostRow
	if err := database.Where("owner_id = ''").Order("id").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var conflicts int64
		if err := database.Model(&store.KnownHostRow{}).
			Where("owner_id = ? AND host = ? AND port = ? AND key_type = ?", ownerID, row.Host, row.Port, row.KeyType).
			Count(&conflicts).Error; err != nil {
			return err
		}
		if conflicts > 0 {
			report.Conflicts++
			continue
		}
		result := database.Model(&store.KnownHostRow{}).
			Where("id = ? AND owner_id = ''", row.ID).
			UpdateColumn("owner_id", ownerID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			report.Assigned++
			continue
		}
		var currentOwner string
		if err := database.Model(&store.KnownHostRow{}).Select("owner_id").Where("id = ?", row.ID).Scan(&currentOwner).Error; err != nil {
			return err
		}
		if currentOwner != "" {
			report.Conflicts++
		}
	}
	return nil
}
