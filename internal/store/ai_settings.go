package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

const (
	AIProfilesSettingKey     = "ai.models"
	AILegacySettingKey       = "ai.provider"
	AIScrubPendingSettingKey = "ai.migration.scrub_pending"
	AIGenerationSettingKey   = "ai.migration.generation"
)

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
