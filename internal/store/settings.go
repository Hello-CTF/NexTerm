package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

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
	if reservedSettingKey(key) {
		return reservedSettingKeyError(key)
	}
	if sensitiveAISettingKey(key) {
		return s.applyAISettingWrites(ctx, map[string]string{key: value}, nil)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, ids.NowMS())
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) applyAISettingWrites(ctx context.Context, values map[string]string, deleteKeys []string) (returnErr error) {
	if err := reservedKeyInWrites(values, deleteKeys); err != nil {
		return err
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
	sensitive := false
	if s.SecretProtector() != nil {
		for key, value := range values {
			if !sensitiveAISettingKey(key) {
				continue
			}
			if AISettingValueSensitive(key, value) {
				sensitive = true
				continue
			}
			current, found, err := txSettingGet(ctx, tx, key)
			if err != nil {
				return err
			}
			if found && AISettingValueSensitive(key, current) {
				sensitive = true
			}
		}
		for _, key := range deleteKeys {
			if !sensitiveAISettingKey(key) {
				continue
			}
			current, found, err := txSettingGet(ctx, tx, key)
			if err != nil {
				return err
			}
			if found && AISettingValueSensitive(key, current) {
				sensitive = true
			}
		}
	}
	now := ids.NowMS()
	for key, value := range values {
		if err := txSettingUpsert(ctx, tx, key, value, now); err != nil {
			return err
		}
	}
	for _, key := range deleteKeys {
		if _, err := tx.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", key); err != nil {
			return dbError(err)
		}
	}
	generationRaw, _, err := txSettingGet(ctx, tx, AIGenerationSettingKey)
	if err != nil {
		return err
	}
	generation := aiGenerationFromRaw(generationRaw) + 1
	if err := txSettingUpsert(ctx, tx, AIGenerationSettingKey, strconv.FormatInt(generation, 10), now); err != nil {
		return err
	}
	if sensitive {
		if err := txSettingUpsert(ctx, tx, AIScrubPendingSettingKey, "1", now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func txSettingGet(ctx context.Context, tx *sql.Tx, key string) (string, bool, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", key).Scan(&value)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dbError(err)
	}
	return value, true, nil
}

func txSettingUpsert(ctx context.Context, tx *sql.Tx, key, value string, now int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) SettingSetMany(ctx context.Context, values map[string]string) (returnErr error) {
	if len(values) == 0 {
		return nil
	}
	if err := reservedKeyInWrites(values, nil); err != nil {
		return err
	}
	for key := range values {
		if sensitiveAISettingKey(key) {
			return s.applyAISettingWrites(ctx, values, nil)
		}
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
	if err := reservedKeyInWrites(values, deleteKeys); err != nil {
		return err
	}
	for key := range values {
		if sensitiveAISettingKey(key) {
			return s.applyAISettingWrites(ctx, values, deleteKeys)
		}
	}
	for _, key := range deleteKeys {
		if sensitiveAISettingKey(key) {
			return s.applyAISettingWrites(ctx, values, deleteKeys)
		}
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
	if reservedSettingKey(key) {
		return reservedSettingKeyError(key)
	}
	if sensitiveAISettingKey(key) {
		return s.applyAISettingWrites(ctx, nil, []string{key})
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", key)
	if err != nil {
		return dbError(err)
	}
	return nil
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
	if err := fn(&txSetting{tx: tx, aiMarking: s.SecretProtector() != nil}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

type txSetting struct {
	tx        *sql.Tx
	aiMarking bool
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
	if reservedSettingKey(key) {
		return reservedSettingKeyError(key)
	}
	if t.aiMarking && sensitiveAISettingKey(key) {
		current, found, err := txSettingGet(ctx, t.tx, key)
		if err != nil {
			return err
		}
		generationRaw, _, err := txSettingGet(ctx, t.tx, AIGenerationSettingKey)
		if err != nil {
			return err
		}
		if err := txSettingUpsert(ctx, t.tx, AIGenerationSettingKey, strconv.FormatInt(aiGenerationFromRaw(generationRaw)+1, 10), ids.NowMS()); err != nil {
			return err
		}
		if found && AISettingValueSensitive(key, current) {
			if err := txSettingUpsert(ctx, t.tx, AIScrubPendingSettingKey, "1", ids.NowMS()); err != nil {
				return err
			}
		}
	}
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
	err = tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", LayoutKey).Scan(&current)
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
