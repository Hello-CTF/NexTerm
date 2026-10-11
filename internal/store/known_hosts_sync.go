package store

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	syncKnownHostOptInPrefix = "sync.optin.known_host."
	syncAIProfileOptInPrefix = "sync.optin.ai_profile."
)

const (
	SyncTombstoneKindKnownHost = "known_host"
	SyncTombstoneKindAIProfile = "ai_profile"
)

func knownHostOptInKey(userID string) string { return syncKnownHostOptInPrefix + userID }
func aiProfileOptInKey(userID string) string { return syncAIProfileOptInPrefix + userID }

func SyncOptInUserID(ctx context.Context) (string, bool) {
	return ipc.UserIDFromContext(ctx)
}

func (s *Store) syncTombstonePut(ctx context.Context, db *gorm.DB, id, kind string, deletedAt int64) error {
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"deleted_at": gorm.Expr(s.dialect.ScalarMax() + "(sync_tombstone.deleted_at, excluded.deleted_at)"),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: s.dialect.conflictTargetColumn("sync_tombstone", "owner_id"), Value: ipc.OwnerID(ctx)}}},
	}).Create(&SyncTombstone{ID: id, Kind: kind, DeletedAt: deletedAt, OwnerID: ipc.OwnerID(ctx)}).Error
}

func (s *Store) SyncTombstoneRecord(ctx context.Context, id, kind string, deletedAt int64) error {
	if err := s.syncTombstonePut(ctx, s.db, id, kind, deletedAt); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) syncOptInGet(ctx context.Context, key string) (bool, error) {
	raw, found, err := s.SettingGet(ctx, key)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return raw == "1", nil
}

func syncOptInValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func (s *Store) KnownHostSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("known_host 同步 opt-in 需要用户身份"))
	}
	return s.syncOptInGet(ctx, knownHostOptInKey(userID))
}

func (s *Store) SetKnownHostSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("known_host 同步 opt-in 需要用户身份"))
	}
	return s.SettingSet(ctx, knownHostOptInKey(userID), syncOptInValue(enabled))
}

func (s *Store) AIProfileSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("AI 模型档案同步 opt-in 需要用户身份"))
	}
	return s.syncOptInGet(ctx, aiProfileOptInKey(userID))
}

func (s *Store) SetAIProfileSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("AI 模型档案同步 opt-in 需要用户身份"))
	}
	return s.SettingSet(ctx, aiProfileOptInKey(userID), syncOptInValue(enabled))
}

func (s *Store) AIPermissionSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("AI 权限同步 opt-in 需要用户身份"))
	}
	return s.syncOptInGet(ctx, "sync.optin.ai_permission."+userID)
}

func (s *Store) SetAIPermissionSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("AI 权限同步 opt-in 需要用户身份"))
	}
	return s.SettingSet(ctx, "sync.optin.ai_permission."+userID, syncOptInValue(enabled))
}

func (s *Store) PreferencesSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("外观与快捷键同步 opt-in 需要用户身份"))
	}
	return s.syncOptInGet(ctx, "sync.optin.preferences."+userID)
}

func (s *Store) SetPreferencesSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("外观与快捷键同步 opt-in 需要用户身份"))
	}
	return s.SettingSet(ctx, "sync.optin.preferences."+userID, syncOptInValue(enabled))
}

func (s *Store) AIProfilesDeleteTx(ctx context.Context, stateJSON string, tombstoneID string, deletedAt int64) error {
	userID, hasIdentity := SyncOptInUserID(ctx)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		optIn := hasIdentity
		if hasIdentity {
			setting := query.Use(tx).Setting
			row, err := setting.WithContext(ctx).Where(setting.Key.Eq(aiProfileOptInKey(userID))).First()
			if err != nil && !isNoRows(err) {
				return err
			}
			if err == nil {
				optIn = row.Value == "1"
			}
		}
		if err := settingUpsert(ctx, tx, AIProfilesSettingKeyForUser(userID), stateJSON, deletedAt); err != nil {
			return err
		}
		if !optIn {
			return nil
		}
		return s.syncTombstonePut(ctx, tx, tombstoneID, SyncTombstoneKindAIProfile, deletedAt)
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}
