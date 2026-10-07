package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const AIProfilesSettingKey = "ai.models"

func (s *Store) SettingGet(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", key).Scan(&value)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dbError(err)
	}
	return value, true, nil
}

func (s *Store) SettingSet(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, ids.NowMS())
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingSetMany(ctx context.Context, values map[string]string) (returnErr error) {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	now := ids.NowMS()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now); err != nil {
			return dbError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) (returnErr error) {
	if len(values) == 0 && len(deleteKeys) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	now := ids.NowMS()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now); err != nil {
			return dbError(err)
		}
	}
	for _, key := range deleteKeys {
		if _, err := tx.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", key); err != nil {
			return dbError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingDelete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", key)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingListPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM setting WHERE key LIKE ? ESCAPE '\'`, escapeLikePrefix(prefix)+"%")
	if err != nil {
		return nil, dbError(err)
	}
	defer func() { _ = rows.Close() }()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, dbError(err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	return values, nil
}

func escapeLikePrefix(prefix string) string {
	var out strings.Builder
	out.Grow(len(prefix))
	for _, r := range prefix {
		switch r {
		case '\\', '%', '_':
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

type SettingTx interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingDelete(ctx context.Context, key string) error
}

func (s *Store) SettingTx(ctx context.Context, fn func(SettingTx) error) (returnErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&txSetting{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

type txSetting struct {
	tx *sql.Tx
}

func (t *txSetting) SettingGet(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", key).Scan(&value)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dbError(err)
	}
	return value, true, nil
}

func (t *txSetting) SettingDelete(ctx context.Context, key string) error {
	if _, err := t.tx.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", key); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) LayoutLoad(ctx context.Context) (int64, int64, json.RawMessage, error) {
	var value string
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, "SELECT value, updated_at FROM setting WHERE key = ?", LayoutKey).
		Scan(&value, &updatedAt)
	if isNoRows(err) {
		return 0, 0, nil, nil
	}
	if err != nil {
		return 0, 0, nil, dbError(err)
	}
	revision, data := decodeLayout(value)
	return revision, updatedAt, data, nil
}

func (s *Store) LayoutSave(ctx context.Context, expected int64, dataJSON string) (saved bool, revision int64, returnErr error) {
	var data any
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return false, 0, badParam(fmt.Errorf("布局不是合法 JSON: %w", err))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()

	var currentRevision int64
	var current string
	err = tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?"+s.dialect.forUpdate(), LayoutKey).Scan(&current)
	if err != nil && !isNoRows(err) {
		return false, 0, dbError(err)
	}
	if err == nil {
		currentRevision, _ = decodeLayout(current)
	}
	if currentRevision != expected {
		_ = tx.Rollback()
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		LayoutKey, string(value), now); err != nil {
		return false, currentRevision, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return false, currentRevision, dbError(err)
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
