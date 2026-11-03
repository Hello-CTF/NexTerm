package alerts

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

type stateKey struct {
	device string
	rule   Rule
}

func loadSettings(ctx context.Context, db *gorm.DB) (map[string]*Settings, error) {
	rows, err := query.Use(db).DeviceAlertSettings.WithContext(ctx).Find()
	if err != nil {
		return nil, fmt.Errorf("load alert settings: %w", err)
	}
	out := make(map[string]*Settings, len(rows))
	for _, r := range rows {
		out[r.DeviceID] = r
	}
	return out, nil
}

// putSettings replaces a device's overrides row.
func putSettings(ctx context.Context, db *gorm.DB, s *Settings) error {
	err := query.Use(db).DeviceAlertSettings.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}},
		UpdateAll: true,
	}).Create(s)
	if err != nil {
		return fmt.Errorf("save alert settings: %w", err)
	}
	return nil
}

func loadStates(ctx context.Context, db *gorm.DB) (map[stateKey]*model.DeviceAlertState, error) {
	rows, err := query.Use(db).DeviceAlertState.WithContext(ctx).Find()
	if err != nil {
		return nil, fmt.Errorf("load alert states: %w", err)
	}
	out := make(map[stateKey]*model.DeviceAlertState, len(rows))
	for _, r := range rows {
		out[stateKey{r.DeviceID, Rule(r.RuleKey)}] = r
	}
	return out, nil
}

func upsertState(ctx context.Context, tx *gorm.DB, s *model.DeviceAlertState) error {
	return query.Use(tx).DeviceAlertState.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "rule_key"}},
		UpdateAll: true,
	}).Create(s)
}

func insertEvent(ctx context.Context, tx *gorm.DB, e *model.DeviceAlertEvent) error {
	return query.Use(tx).DeviceAlertEvent.WithContext(ctx).Create(e)
}

// deleteRevokedStates silently drops states of revoked devices.
func deleteRevokedStates(ctx context.Context, db *gorm.DB) error {
	q := query.Use(db)
	revoked := q.UserDevice.WithContext(ctx).Select(q.UserDevice.ID).Where(q.UserDevice.RevokedAt.IsNotNull())
	if _, err := q.DeviceAlertState.WithContext(ctx).
		Where(q.DeviceAlertState.Columns(q.DeviceAlertState.DeviceID).In(revoked)).
		Delete(); err != nil {
		return fmt.Errorf("delete revoked alert states: %w", err)
	}
	return nil
}

func deleteEventsBefore(ctx context.Context, db *gorm.DB, cutoff int64) error {
	q := query.Use(db)
	if _, err := q.DeviceAlertEvent.WithContext(ctx).Where(q.DeviceAlertEvent.TS.Lt(cutoff)).Delete(); err != nil {
		return fmt.Errorf("delete old alert events: %w", err)
	}
	return nil
}

// ackFired acknowledges the device's unacknowledged fired events of one rule; a resolve supersedes them.
func ackFired(ctx context.Context, tx *gorm.DB, deviceID, rule string, now int64) error {
	q := query.Use(tx)
	_, err := q.DeviceAlertEvent.WithContext(ctx).
		Where(q.DeviceAlertEvent.DeviceID.Eq(deviceID), q.DeviceAlertEvent.RuleKey.Eq(rule),
			q.DeviceAlertEvent.Kind.Eq(KindFired), q.DeviceAlertEvent.AckedAt.IsNull()).
		Update(q.DeviceAlertEvent.AckedAt, now)
	if err != nil {
		return fmt.Errorf("ack fired alert events: %w", err)
	}
	return nil
}

const evaluatorStateID = "default"

// loadLastTick returns the previous run's last evaluation time, or 0 when none was recorded.
func loadLastTick(ctx context.Context, db *gorm.DB) (int64, error) {
	q := query.Use(db)
	row, err := q.AlertEvaluatorState.WithContext(ctx).Where(q.AlertEvaluatorState.ID.Eq(evaluatorStateID)).Take()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load alert evaluator state: %w", err)
	}
	return row.LastTickAt, nil
}

// saveLastTick records the evaluation time.
func saveLastTick(ctx context.Context, db *gorm.DB, at int64) error {
	row := &model.AlertEvaluatorState{ID: evaluatorStateID, LastTickAt: at, UpdatedAt: at}
	err := query.Use(db).AlertEvaluatorState.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
	}).Create(row)
	if err != nil {
		return fmt.Errorf("save alert evaluator state: %w", err)
	}
	return nil
}
