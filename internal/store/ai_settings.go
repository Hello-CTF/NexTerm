package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	AIProfilesSettingKey     = "ai.models"
	AILegacySettingKey       = "ai.provider"
	AIScrubPendingSettingKey = "ai.migration.scrub_pending"
	AIGenerationSettingKey   = "ai.migration.generation"
)

var ErrReservedSettingKey = errors.New("内部协议字段不可写入")

func reservedSettingKey(key string) bool {
	return strings.HasPrefix(key, "ai.migration.")
}

func reservedSettingKeyError(key string) error {
	return fmt.Errorf("%w: %s", ErrReservedSettingKey, key)
}

func reservedKeyInWrites(values map[string]string, deleteKeys []string) error {
	for key := range values {
		if reservedSettingKey(key) {
			return reservedSettingKeyError(key)
		}
	}
	for _, key := range deleteKeys {
		if reservedSettingKey(key) {
			return reservedSettingKeyError(key)
		}
	}
	return nil
}

func sensitiveAISettingKey(key string) bool {
	return key == AIProfilesSettingKey || key == AILegacySettingKey
}

func AISettingValueSensitive(key, value string) bool {
	switch key {
	case AIProfilesSettingKey:
		return aiProfilesValueSensitive(value)
	case AILegacySettingKey:
		return aiLegacyValueSensitive(value)
	}
	return false
}

func aiProfilesValueSensitive(value string) bool {
	var envelope struct {
		Profiles []struct {
			APIKey string `json:"apiKey"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(value), &envelope); err != nil {
		return true
	}
	for _, profile := range envelope.Profiles {
		if profile.APIKey != "" && !strings.HasPrefix(profile.APIKey, SecretEnvelopePrefix) {
			return true
		}
	}
	return false
}

func aiLegacyValueSensitive(value string) bool {
	var legacy struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal([]byte(value), &legacy); err != nil {
		return true
	}
	return legacy.APIKey != ""
}

func aiGenerationFromRaw(raw string) int64 {
	generation, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return generation
}

func (s *Store) AISettingsGeneration(ctx context.Context) (int64, error) {
	raw, found, err := s.SettingGet(ctx, AIGenerationSettingKey)
	if err != nil || !found {
		return 0, err
	}
	return aiGenerationFromRaw(raw), nil
}

func (s *Store) AISettingsMarkPending(ctx context.Context) (returnErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback()
		}
	}()
	generationRaw, _, err := txSettingGet(ctx, tx, AIGenerationSettingKey)
	if err != nil {
		return err
	}
	now := ids.NowMS()
	if err := txSettingUpsert(ctx, tx, AIGenerationSettingKey, strconv.FormatInt(aiGenerationFromRaw(generationRaw)+1, 10), now); err != nil {
		return err
	}
	if err := txSettingUpsert(ctx, tx, AIScrubPendingSettingKey, "1", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) AISettingsFinalizeScrub(ctx context.Context, expectedGeneration int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, dbError(err)
	}
	pendingRaw, found, err := txSettingGet(ctx, tx, AIScrubPendingSettingKey)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if !found || pendingRaw == "" {
		return false, tx.Commit()
	}
	generationRaw, _, err := txSettingGet(ctx, tx, AIGenerationSettingKey)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if aiGenerationFromRaw(generationRaw) != expectedGeneration {
		return false, tx.Commit()
	}
	legacyRaw, legacyFound, err := txSettingGet(ctx, tx, AILegacySettingKey)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if legacyFound && AISettingValueSensitive(AILegacySettingKey, legacyRaw) {
		return false, tx.Commit()
	}
	modelsRaw, modelsFound, err := txSettingGet(ctx, tx, AIProfilesSettingKey)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if modelsFound && AISettingValueSensitive(AIProfilesSettingKey, modelsRaw) {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM setting WHERE key = ?", AIScrubPendingSettingKey); err != nil {
		_ = tx.Rollback()
		return false, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return false, dbError(err)
	}
	return true, nil
}
