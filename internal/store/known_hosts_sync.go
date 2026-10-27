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
	SyncTombstoneKindAIGrant   = "ai_grant"
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
			"deleted_at": MaxExpression(db, "sync_tombstone", "deleted_at", nil),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
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
		return false, badParam(fmt.Errorf("开启已知主机同步需要先登录账号"))
	}
	return s.syncOptInGet(ctx, knownHostOptInKey(userID))
}

func (s *Store) SetKnownHostSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("开启已知主机同步需要先登录账号"))
	}
	return s.SettingSet(ctx, knownHostOptInKey(userID), syncOptInValue(enabled))
}

func (s *Store) AIProfileSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("开启 AI 模型档案同步需要先登录账号"))
	}
	return s.syncOptInGet(ctx, aiProfileOptInKey(userID))
}

func (s *Store) SetAIProfileSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("开启 AI 模型档案同步需要先登录账号"))
	}
	return s.SettingSet(ctx, aiProfileOptInKey(userID), syncOptInValue(enabled))
}

func (s *Store) AIPermissionSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("开启 AI 权限同步需要先登录账号"))
	}
	return s.syncOptInGet(ctx, "sync.optin.ai_permission."+userID)
}

func (s *Store) SetAIPermissionSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("开启 AI 权限同步需要先登录账号"))
	}
	return s.SettingSet(ctx, "sync.optin.ai_permission."+userID, syncOptInValue(enabled))
}

func (s *Store) PreferencesSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("开启外观与快捷键同步需要先登录账号"))
	}
	return s.syncOptInGet(ctx, "sync.optin.preferences."+userID)
}

func (s *Store) SetPreferencesSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("开启外观与快捷键同步需要先登录账号"))
	}
	return s.SettingSet(ctx, "sync.optin.preferences."+userID, syncOptInValue(enabled))
}

func (s *Store) AIGrantSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, badParam(fmt.Errorf("开启 AI 授权同步需要先登录账号"))
	}
	return s.syncOptInGet(ctx, "sync.optin.ai_grant."+userID)
}

func (s *Store) SetAIGrantSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if userID == "" {
		return badParam(fmt.Errorf("开启 AI 授权同步需要先登录账号"))
	}
	return s.SettingSet(ctx, "sync.optin.ai_grant."+userID, syncOptInValue(enabled))
}

// syncOptInGetOr reads an opt-in switch that falls back to fallback while unset.
func (s *Store) syncOptInGetOr(ctx context.Context, key string, fallback bool) (bool, error) {
	raw, found, err := s.SettingGet(ctx, key)
	if err != nil {
		return false, err
	}
	if !found {
		return fallback, nil
	}
	return raw == "1", nil
}

func syncOptInUser(userID, what string) error {
	if userID == "" {
		return badParam(fmt.Errorf("开启%s同步需要先登录账号", what))
	}
	return nil
}

func (s *Store) StateRuleSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if err := syncOptInUser(userID, "状态规则"); err != nil {
		return false, err
	}
	return s.syncOptInGetOr(ctx, "sync.optin.state_rule."+userID, true)
}

func (s *Store) SetStateRuleSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if err := syncOptInUser(userID, "状态规则"); err != nil {
		return err
	}
	return s.SettingSet(ctx, "sync.optin.state_rule."+userID, syncOptInValue(enabled))
}

func (s *Store) RunbookTemplateSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if err := syncOptInUser(userID, " Runbook 模板"); err != nil {
		return false, err
	}
	return s.syncOptInGetOr(ctx, "sync.optin.runbook_template."+userID, true)
}

func (s *Store) SetRunbookTemplateSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if err := syncOptInUser(userID, " Runbook 模板"); err != nil {
		return err
	}
	return s.SettingSet(ctx, "sync.optin.runbook_template."+userID, syncOptInValue(enabled))
}

func (s *Store) SkillSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if err := syncOptInUser(userID, " AI 技能"); err != nil {
		return false, err
	}
	return s.syncOptInGetOr(ctx, "sync.optin.skill."+userID, true)
}

func (s *Store) SetSkillSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if err := syncOptInUser(userID, " AI 技能"); err != nil {
		return err
	}
	return s.SettingSet(ctx, "sync.optin.skill."+userID, syncOptInValue(enabled))
}

// MemorySyncOptIn defaults off: AI memory may hold sensitive notes, so it syncs only when the user opts in.
func (s *Store) MemorySyncOptIn(ctx context.Context, userID string) (bool, error) {
	if err := syncOptInUser(userID, " AI 记忆"); err != nil {
		return false, err
	}
	return s.syncOptInGetOr(ctx, "sync.optin.memory."+userID, false)
}

func (s *Store) SetMemorySyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if err := syncOptInUser(userID, " AI 记忆"); err != nil {
		return err
	}
	return s.SettingSet(ctx, "sync.optin.memory."+userID, syncOptInValue(enabled))
}

// CronSyncOptIn defaults off: synced scheduled jobs carry AI prompts and only ever run on the device they are bound to.
func (s *Store) CronSyncOptIn(ctx context.Context, userID string) (bool, error) {
	if err := syncOptInUser(userID, "定时任务"); err != nil {
		return false, err
	}
	return s.syncOptInGetOr(ctx, "sync.optin.cron."+userID, false)
}

func (s *Store) SetCronSyncOptIn(ctx context.Context, userID string, enabled bool) error {
	if err := syncOptInUser(userID, "定时任务"); err != nil {
		return err
	}
	return s.SettingSet(ctx, "sync.optin.cron."+userID, syncOptInValue(enabled))
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
