package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const AIProfilesSettingKey = "ai.models"

func AIProfilesSettingKeyForUser(userID string) string {
	if userID == "" {
		return AIProfilesSettingKey
	}
	return "user.ai.models." + userID
}

func LayoutSettingKeyForUser(userID string) string {
	if userID == "" {
		return LayoutKey
	}
	return "user.layout." + userID
}

func settingUpsert(ctx context.Context, db *gorm.DB, key, value string, updatedAt int64) error {
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&Setting{Key: key, Value: value, UpdatedAt: updatedAt}).Error
}

func (s *Store) SettingGet(ctx context.Context, key string) (string, bool, error) {
	setting := query.Use(s.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).First()
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dbError(err)
	}
	return row.Value, true, nil
}

func (s *Store) SettingSet(ctx context.Context, key, value string) error {
	if err := settingUpsert(ctx, s.db, key, value, ids.NowMS()); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingSetMany(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := ids.NowMS()
		for key, value := range values {
			if err := settingUpsert(ctx, tx, key, value, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) error {
	if len(values) == 0 && len(deleteKeys) == 0 {
		return nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := ids.NowMS()
		for key, value := range values {
			if err := settingUpsert(ctx, tx, key, value, now); err != nil {
				return err
			}
		}
		setting := query.Use(tx).Setting
		for _, key := range deleteKeys {
			if _, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).Delete(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingDelete(ctx context.Context, key string) error {
	setting := query.Use(s.db).Setting
	_, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingListPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := SettingRowsWithPrefix(ctx, s.db, prefix)
	if err != nil {
		return nil, dbError(err)
	}
	values := map[string]string{}
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	return values, nil
}

func SettingRowsWithPrefix(ctx context.Context, db *gorm.DB, prefix string) ([]Setting, error) {
	setting := query.Use(db).Setting
	rows := []Setting{}
	if err := setting.WithContext(ctx).
		Where(settingLike("key", EscapeLikePrefix(prefix)+"%")).
		Scan(&rows); err != nil {
		return nil, err
	}
	exact := rows[:0]
	for _, row := range rows {
		if strings.HasPrefix(row.Key, prefix) {
			exact = append(exact, row)
		}
	}
	return exact, nil
}

func EscapeLikePrefix(prefix string) string {
	var out strings.Builder
	out.Grow(len(prefix))
	for _, r := range prefix {
		switch r {
		case '!', '%', '_':
			out.WriteByte('!')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func settingLike(column, pattern string) field.Expr {
	return field.NewUnsafeFieldRaw("? LIKE ? ESCAPE '!'", clause.Column{Name: column}, pattern)
}

func (s *Store) SettingContainsValue(ctx context.Context, substring string) (bool, error) {
	setting := query.Use(s.db).Setting
	rows := []Setting{}
	err := setting.WithContext(ctx).
		Where(settingLike("value", "%"+EscapeLikePrefix(substring)+"%")).
		Scan(&rows)
	if err != nil {
		return false, dbError(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Value, substring) {
			return true, nil
		}
	}
	return false, nil
}

type SettingTx interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingDelete(ctx context.Context, key string) error
}

func (s *Store) SettingTx(ctx context.Context, fn func(SettingTx) error) error {
	var callbackErr error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		callbackErr = fn(&txSetting{tx: tx})
		return callbackErr
	})
	if callbackErr != nil {
		return callbackErr
	}
	if err != nil {
		return dbError(err)
	}
	return nil
}

type txSetting struct {
	tx *gorm.DB
}

func (t *txSetting) SettingGet(ctx context.Context, key string) (string, bool, error) {
	setting := query.Use(t.tx).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).First()
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dbError(err)
	}
	return row.Value, true, nil
}

func (t *txSetting) SettingDelete(ctx context.Context, key string) error {
	setting := query.Use(t.tx).Setting
	_, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) LayoutLoad(ctx context.Context) (int64, int64, json.RawMessage, error) {
	setting := query.Use(s.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(LayoutSettingKeyForUser(ipc.OwnerID(ctx)))).First()
	if isNoRows(err) {
		return 0, 0, nil, nil
	}
	if err != nil {
		return 0, 0, nil, dbError(err)
	}
	revision, data := decodeLayout(row.Value)
	return revision, row.UpdatedAt, data, nil
}

func (s *Store) LayoutSave(ctx context.Context, expected int64, dataJSON string) (bool, int64, error) {
	var data any
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return false, 0, badParam(fmt.Errorf("布局不是合法 JSON: %w", err))
	}
	key := LayoutSettingKeyForUser(ipc.OwnerID(ctx))
	setting := query.Use(s.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).First()
	if err != nil && !isNoRows(err) {
		return false, 0, dbError(err)
	}
	var currentRevision int64
	if row != nil {
		currentRevision, _ = decodeLayout(row.Value)
	}
	if currentRevision != expected {
		return false, currentRevision, nil
	}
	next := currentRevision + 1
	now := ids.NowMS()
	value, err := json.Marshal(struct {
		Revision  int64 `json:"revision"`
		UpdatedAt int64 `json:"updatedAt"`
		Data      any   `json:"data"`
	}{Revision: next, UpdatedAt: now, Data: data})
	if err != nil {
		return false, currentRevision, dbError(err)
	}
	conflict := func() (bool, int64, error) {
		revision, _, _, err := s.LayoutLoad(ctx)
		return false, revision, err
	}
	if row == nil {
		err = s.db.WithContext(ctx).Create(&Setting{Key: key, Value: string(value), UpdatedAt: now}).Error
		if err == nil {
			return true, next, nil
		}
		if IsUniqueErr(err) {
			return conflict()
		}
		return false, currentRevision, dbError(err)
	}
	result := s.db.WithContext(ctx).Model(&Setting{}).
		Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: key}).
		Where(clause.Eq{Column: clause.Column{Name: "value"}, Value: row.Value}).
		UpdateColumns(map[string]any{"value": string(value), "updated_at": now})
	if result.Error != nil {
		return false, currentRevision, dbError(result.Error)
	}
	if result.RowsAffected != 1 {
		return conflict()
	}
	return true, next, nil
}

func decodeLayout(value string) (int64, json.RawMessage) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &envelope); err != nil {
		return 0, nil
	}
	var revision int64
	if raw := envelope["revision"]; raw != nil {
		_ = json.Unmarshal(raw, &revision)
	}
	data := envelope["data"]
	if len(data) == 0 || string(data) == "null" || !json.Valid(data) {
		return revision, nil
	}
	return revision, data
}
