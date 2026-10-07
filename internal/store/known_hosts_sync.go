package store

import (
	"context"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// known_host 与 AI 模型档案的同步 opt-in 开关键, 按账号用户隔离(沿用 sync.cursor.<userID> 的键先例):
// 键形如 sync.optin.known_host.<userID>, 缺行即关闭(默认不同步主机信任与 AI 档案), 用户 A 的选择不影响用户 B。
const (
	syncKnownHostOptInPrefix = "sync.optin.known_host."
	syncAIProfileOptInPrefix = "sync.optin.ai_profile."
)

// 用户主动删除墓碑的 kind 值, 与 internal/sync 的 KindKnownHost/KindAIProfile 同值
// (store 不能反向依赖 sync 包, 常量在此对齐)。
const (
	SyncTombstoneKindKnownHost = "known_host"
	SyncTombstoneKindAIProfile = "ai_profile"
)

func knownHostOptInKey(userID string) string { return syncKnownHostOptInPrefix + userID }
func aiProfileOptInKey(userID string) string { return syncAIProfileOptInPrefix + userID }

// SyncOptInUserID 从 ctx 取会话注入的用户身份; 无身份(桌面 wails 直连、匿名模式)返回 false,
// 调用方按 opt-in 关闭处理; 删除钩子不得退化为全局设置。
func SyncOptInUserID(ctx context.Context) (string, bool) {
	return ipc.UserIDFromContext(ctx)
}

// SyncTombstoneRecord 记录用户主动删除产生的同步墓碑, 修订号按方言取较大值(与引擎 syncTombstonePut 同语义)。
// 仅供已 opt-in 的 known_host/AI 模型档案删除路径使用; 用户删除墓碑不带冲突三元组,
// known_host 冲突墓碑的三元组元数据由 internal/sync 侧管理, 不经过这里。
func (s *Store) SyncTombstoneRecord(ctx context.Context, id, kind string, deletedAt int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at = `+s.dialect.ScalarMax()+`(sync_tombstone.deleted_at, excluded.deleted_at)`,
		id, kind, deletedAt)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) syncOptInGet(ctx context.Context, key string) (bool, error) {
	raw, found, err := s.SettingGet(ctx, key)
	if err != nil || !found {
		return false, err
	}
	return raw == "1", nil
}

func syncOptInValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

// KnownHostSyncOptIn 读取指定用户的 known_host 同步 opt-in; userID 为空拒绝服务, 防止退化为全局键。
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

// AIProfilesDeleteTx 在同一事务内写回 ai.models 状态, 并按 ctx 用户的 opt-in 记录删除墓碑(用户删除语义)。
// 原子性保证档案删除与墓碑同成同败: 任一步失败整体回滚, 档案仍在, 重试 Delete 即可收敛,
// 不会出现「档案已删但墓碑缺失」导致远端副本复活。
func (s *Store) AIProfilesDeleteTx(ctx context.Context, stateJSON string, tombstoneID string, deletedAt int64) error {
	userID, hasIdentity := SyncOptInUserID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	optIn := false
	if hasIdentity {
		var optInRaw string
		optInErr := tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", aiProfileOptInKey(userID)).Scan(&optInRaw)
		if optInErr != nil && !isNoRows(optInErr) {
			return dbError(optInErr)
		}
		optIn = optInRaw == "1"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		AIProfilesSettingKey, stateJSON, deletedAt); err != nil {
		return dbError(err)
	}
	if optIn {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at = `+s.dialect.ScalarMax()+`(sync_tombstone.deleted_at, excluded.deleted_at)`,
			tombstoneID, SyncTombstoneKindAIProfile, deletedAt); err != nil {
			return dbError(err)
		}
	}
	return tx.Commit()
}
