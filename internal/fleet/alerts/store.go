package alerts

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// The queries on device_alert_* tables below use db.Model/Table because GORM Gen code does not exist
// for the new models yet; each is marked GEN-TODO and moves to query.Use(db) once generated.

type stateKey struct {
	device string
	rule   Rule
}

// GEN-TODO: DeviceAlertSettings find-all.
func loadSettings(ctx context.Context, db *gorm.DB) (map[string]*Settings, error) {
	var rows []*Settings
	if err := db.WithContext(ctx).Model(&model.DeviceAlertSettings{}).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load alert settings: %w", err)
	}
	out := make(map[string]*Settings, len(rows))
	for _, r := range rows {
		out[r.DeviceID] = r
	}
	return out, nil
}

// putSettings replaces a device's overrides row.
// GEN-TODO: DeviceAlertSettings upsert.
func putSettings(ctx context.Context, db *gorm.DB, s *Settings) error {
	err := db.WithContext(ctx).Model(&model.DeviceAlertSettings{}).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}},
		UpdateAll: true,
	}).Create(s).Error
	if err != nil {
		return fmt.Errorf("save alert settings: %w", err)
	}
	return nil
}

// GEN-TODO: DeviceAlertState find-all.
func loadStates(ctx context.Context, db *gorm.DB) (map[stateKey]*model.DeviceAlertState, error) {
	var rows []*model.DeviceAlertState
	if err := db.WithContext(ctx).Model(&model.DeviceAlertState{}).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load alert states: %w", err)
	}
	out := make(map[stateKey]*model.DeviceAlertState, len(rows))
	for _, r := range rows {
		out[stateKey{r.DeviceID, Rule(r.RuleKey)}] = r
	}
	return out, nil
}

// GEN-TODO: DeviceAlertState upsert.
func upsertState(ctx context.Context, tx *gorm.DB, s *model.DeviceAlertState) error {
	return tx.WithContext(ctx).Model(&model.DeviceAlertState{}).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "rule_key"}},
		UpdateAll: true,
	}).Create(s).Error
}

// GEN-TODO: DeviceAlertEvent insert.
func insertEvent(ctx context.Context, tx *gorm.DB, e *model.DeviceAlertEvent) error {
	return tx.WithContext(ctx).Model(&model.DeviceAlertEvent{}).Create(e).Error
}

// deleteRevokedStates silently drops states of revoked devices.
// GEN-TODO: DeviceAlertState delete with user_device subquery.
func deleteRevokedStates(ctx context.Context, db *gorm.DB) error {
	sub := db.Table("user_device").Select("id").Where("revoked_at IS NOT NULL")
	if err := db.WithContext(ctx).Where("device_id IN (?)", sub).Delete(&model.DeviceAlertState{}).Error; err != nil {
		return fmt.Errorf("delete revoked alert states: %w", err)
	}
	return nil
}

// GEN-TODO: DeviceAlertEvent delete older than cutoff.
func deleteEventsBefore(ctx context.Context, db *gorm.DB, cutoff int64) error {
	if err := db.WithContext(ctx).Where("ts < ?", cutoff).Delete(&model.DeviceAlertEvent{}).Error; err != nil {
		return fmt.Errorf("delete old alert events: %w", err)
	}
	return nil
}

// ackFired acknowledges the device's unacknowledged fired events of one rule; a resolve supersedes them.
// GEN-TODO: DeviceAlertEvent ack update by device and rule.
func ackFired(ctx context.Context, tx *gorm.DB, deviceID, rule string, now int64) error {
	err := tx.WithContext(ctx).Model(&model.DeviceAlertEvent{}).
		Where("device_id = ? AND rule_key = ? AND kind = ? AND acked_at IS NULL", deviceID, rule, KindFired).
		Update("acked_at", now).Error
	if err != nil {
		return fmt.Errorf("ack fired alert events: %w", err)
	}
	return nil
}
