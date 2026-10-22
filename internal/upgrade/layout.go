package upgrade

import (
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func layoutMigrationPending(database *gorm.DB) (bool, error) {
	var row store.Setting
	err := database.Where(layoutKeyEquals(store.LayoutKey)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func migrateLayout(database *gorm.DB, ownerID string, report *LayoutReport) error {
	var source store.Setting
	err := database.Where(layoutKeyEquals(store.LayoutKey)).First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	targetKey := store.LayoutSettingKeyForUser(ownerID)
	var target store.Setting
	err = database.Where(layoutKeyEquals(targetKey)).First(&target).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		classifyLayoutTarget(source, target, report)
		return nil
	}

	target = store.Setting{Key: targetKey, Value: source.Value, UpdatedAt: source.UpdatedAt}
	result := database.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&target)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		report.Copied++
		return nil
	}
	if err := database.Where(layoutKeyEquals(targetKey)).First(&target).Error; err != nil {
		return err
	}
	classifyLayoutTarget(source, target, report)
	return nil
}

func layoutKeyEquals(key string) clause.Expression {
	return clause.Eq{Column: clause.Column{Name: "key"}, Value: key}
}

func classifyLayoutTarget(source, target store.Setting, report *LayoutReport) {
	if source.Value == target.Value {
		report.Existing++
		return
	}
	report.Conflicts++
}
