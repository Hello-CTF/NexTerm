package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const preferenceObjectPrefix = "preference:"

func (e *Engine) collectPreferenceObjects(ctx context.Context, userID string, objects map[string]localObject, optIn kindOptIn) error {
	if optIn.aiPermission {
		if err := e.collectAIPermission(ctx, objects); err != nil {
			return err
		}
	}
	if optIn.preferences {
		if err := e.collectPreferences(ctx, userID, objects); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) collectAIPermission(ctx context.Context, objects map[string]localObject) error {
	setting := query.Use(e.store.GORM()).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(guard.PermissionSettingKeyForUser(ctx))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	var config guard.Config
	if err := json.Unmarshal([]byte(row.Value), &config); err != nil {
		return ipc.WrapError(ipc.CodeBadParam, "AI 权限配置数据损坏", err)
	}
	config = config.Normalized()
	payload, err := marshalObject(aiPermissionObject{ID: "global", Mode: string(config.Mode), DangerRules: config.DangerRules, UpdatedAt: row.UpdatedAt})
	if err != nil {
		return err
	}
	objects["global"] = localObject{KindAIPermission, payload}
	return nil
}

func (e *Engine) collectPreferences(ctx context.Context, userID string, objects map[string]localObject) error {
	prefix := account.PreferenceUserKeyPrefix(userID)
	setting := query.Use(e.store.GORM()).Setting
	rows := []store.Setting{}
	if err := setting.WithContext(ctx).
		Where(field.NewUnsafeFieldRaw(`key LIKE ? ESCAPE '\'`, prefix+"%")).
		Scan(&rows); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	for _, row := range rows {
		key := strings.TrimPrefix(row.Key, prefix)
		normalized, err := account.NormalizePreferenceValue(key, json.RawMessage(row.Value))
		if err != nil {
			continue
		}
		id := preferenceObjectPrefix + key
		payload, err := marshalObject(preferenceObject{ID: id, Key: key, Value: json.RawMessage(normalized), UpdatedAt: row.UpdatedAt})
		if err != nil {
			return err
		}
		objects[id] = localObject{KindPreference, payload}
	}
	return nil
}

func (e *Engine) applyAIPermissionObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload aiPermissionObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 权限对象载荷损坏: %v", err)
		return false, false
	}
	if payload.ID != "global" {
		report.warnf("AI 权限对象 ID %s 不受支持", payload.ID)
		return false, false
	}
	config := guard.Config{Mode: guard.Mode(payload.Mode), DangerRules: payload.DangerRules}.Normalized()
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查 AI 权限删除墓碑: %v", err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	updatedAt, found, err := e.settingRevision(ctx, guard.PermissionSettingKeyForUser(ctx))
	if err != nil {
		report.warnf("无法读取 AI 权限配置: %v", err)
		return false, false
	}
	if found {
		var local guard.Config
		raw, _, err := e.store.SettingGet(ctx, guard.PermissionSettingKeyForUser(ctx))
		if err != nil {
			report.warnf("无法读取 AI 权限配置: %v", err)
			return false, false
		}
		if err := json.Unmarshal([]byte(raw), &local); err != nil {
			report.warnf("AI 权限配置数据损坏: %v", err)
			return false, false
		}
		local = local.Normalized()
		localPlaintext, err := marshalObject(aiPermissionObject{ID: "global", Mode: string(local.Mode), DangerRules: local.DangerRules, UpdatedAt: updatedAt})
		if err != nil {
			return false, false
		}
		if bytes.Equal(plaintext, localPlaintext) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, updatedAt, plaintext, localPlaintext) {
			return false, false
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return false, false
	}
	if err := e.settingPut(ctx, guard.PermissionSettingKeyForUser(ctx), string(encoded), payload.UpdatedAt); err != nil {
		report.warnf("AI 权限配置应用失败: %v", err)
		return false, false
	}
	if err := e.clearTombstone(ctx, payload.ID); err != nil {
		report.warnf("AI 权限墓碑清理失败: %v", err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyPreferenceObject(ctx context.Context, plaintext []byte, report *SyncReport, userID string) (bool, bool) {
	var payload preferenceObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("偏好对象载荷损坏: %v", err)
		return false, false
	}
	if userID == "" {
		report.warnf("偏好同步需要账号身份")
		return false, false
	}
	if payload.ID != preferenceObjectPrefix+payload.Key {
		report.warnf("偏好对象 ID 与键不匹配: %s", payload.ID)
		return false, false
	}
	normalized, err := account.NormalizePreferenceValue(payload.Key, payload.Value)
	if err != nil {
		report.warnf("偏好 %s 数据无效: %v", payload.Key, err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查偏好 %s 的删除墓碑: %v", payload.Key, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	key := account.PreferenceUserKeyPrefix(userID) + payload.Key
	updatedAt, found, err := e.settingRevision(ctx, key)
	if err != nil {
		report.warnf("无法读取偏好 %s: %v", payload.Key, err)
		return false, false
	}
	if found {
		localValue, _, err := e.store.SettingGet(ctx, key)
		if err != nil {
			report.warnf("无法读取偏好 %s: %v", payload.Key, err)
			return false, false
		}
		localNormalized, err := account.NormalizePreferenceValue(payload.Key, json.RawMessage(localValue))
		if err != nil {
			report.warnf("本地偏好 %s 数据无效: %v", payload.Key, err)
			return false, false
		}
		localPlaintext, err := marshalObject(preferenceObject{ID: payload.ID, Key: payload.Key, Value: json.RawMessage(localNormalized), UpdatedAt: updatedAt})
		if err != nil {
			return false, false
		}
		if bytes.Equal(plaintext, localPlaintext) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, updatedAt, plaintext, localPlaintext) {
			return false, false
		}
	}
	if err := e.settingPut(ctx, key, normalized, payload.UpdatedAt); err != nil {
		report.warnf("偏好 %s 应用失败: %v", payload.Key, err)
		return false, false
	}
	if err := e.clearTombstone(ctx, payload.ID); err != nil {
		report.warnf("偏好 %s 的墓碑清理失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIPermissionTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if objectID != "global" {
		report.warnf("AI 权限墓碑 ID %s 不受支持", objectID)
		return false, false
	}
	updatedAt, found, err := e.settingRevision(ctx, guard.PermissionSettingKeyForUser(ctx))
	if err != nil {
		report.warnf("无法读取 AI 权限配置: %v", err)
		return false, false
	}
	if found && updatedAt > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindAIPermission, deletedAt); err != nil {
		report.warnf("AI 权限删除墓碑记录失败: %v", err)
		return false, false
	}
	if found {
		if err := e.settingDelete(ctx, guard.PermissionSettingKeyForUser(ctx)); err != nil {
			report.warnf("AI 权限配置删除失败: %v", err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyPreferenceTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport, userID string) (bool, bool) {
	if userID == "" {
		report.warnf("偏好删除墓碑需要账号身份")
		return false, false
	}
	preferenceKey := strings.TrimPrefix(objectID, preferenceObjectPrefix)
	if objectID != preferenceObjectPrefix+preferenceKey || !account.PreferenceKeyDeclared(preferenceKey) {
		report.warnf("偏好墓碑 ID %s 不受支持", objectID)
		return false, false
	}
	key := account.PreferenceUserKeyPrefix(userID) + preferenceKey
	updatedAt, found, err := e.settingRevision(ctx, key)
	if err != nil {
		report.warnf("无法读取偏好 %s: %v", objectID, err)
		return false, false
	}
	if found && updatedAt > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindPreference, deletedAt); err != nil {
		report.warnf("偏好 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	if found {
		if err := e.settingDelete(ctx, key); err != nil {
			report.warnf("偏好 %s 删除失败: %v", objectID, err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) settingRevision(ctx context.Context, key string) (int64, bool, error) {
	setting := query.Use(e.store.GORM()).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return row.UpdatedAt, true, nil
}

func (e *Engine) settingPut(ctx context.Context, key, value string, updatedAt int64) error {
	if err := e.store.GORM().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&store.Setting{Key: key, Value: value, UpdatedAt: updatedAt}).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func (e *Engine) settingDelete(ctx context.Context, key string) error {
	setting := query.Use(e.store.GORM()).Setting
	if _, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).Delete(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func (e *Engine) clearTombstone(ctx context.Context, id string) error {
	tombstone := query.Use(e.store.GORM()).SyncTombstone
	if _, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(id)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

type PreferenceScopeView struct {
	Defaults  map[string]json.RawMessage `json:"defaults"`
	Overrides map[string]json.RawMessage `json:"overrides"`
	Effective map[string]json.RawMessage `json:"effective"`
}

type PreferenceUpdate struct {
	Set   map[string]json.RawMessage `json:"set"`
	Clear []string                   `json:"clear"`
}

func (s *Service) preferenceUserID(ctx context.Context) (string, error) {
	if userID, ok := UserIDFromContext(ctx); ok && userID != "" {
		return userID, nil
	}
	s.engine.sessionMu.Lock()
	defer s.engine.sessionMu.Unlock()
	if s.engine.session != nil && s.engine.session.userID != "" {
		return s.engine.session.userID, nil
	}
	return "", ipc.NewError(ipc.CodeBadParam, "同步账号未登录")
}

func (s *Service) preferenceView(ctx context.Context) (PreferenceScopeView, error) {
	userID, err := s.preferenceUserID(ctx)
	if err != nil {
		return PreferenceScopeView{}, err
	}
	preferences := account.NewPreferences(s.store)
	defaults, err := preferences.Defaults(ctx)
	if err != nil {
		return PreferenceScopeView{}, err
	}
	overrides, err := preferences.UserOverrides(ctx, userID)
	if err != nil {
		return PreferenceScopeView{}, err
	}
	return PreferenceScopeView{Defaults: defaults, Overrides: overrides, Effective: account.MergePreferences(defaults, overrides)}, nil
}

func (s *Service) PreferenceView(ctx context.Context) (PreferenceScopeView, error) {
	return s.preferenceView(ctx)
}

func (s *Service) PreferenceUpdate(ctx context.Context, update PreferenceUpdate) (PreferenceScopeView, error) {
	userID, err := s.preferenceUserID(ctx)
	if err != nil {
		return PreferenceScopeView{}, err
	}
	if _, err := account.NewPreferences(s.store).UpdateUserOverrides(ctx, userID, update.Set, update.Clear); err != nil {
		return PreferenceScopeView{}, err
	}
	return s.preferenceView(ctx)
}

type CollectAIPermission struct {
	ID          string   `json:"id"`
	Mode        string   `json:"mode"`
	DangerRules []string `json:"dangerRules"`
	UpdatedAt   int64    `json:"updatedAt"`
	Found       bool     `json:"found"`
}

type CollectPreference struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt int64           `json:"updatedAt"`
}

func (s *Service) CollectAIPermission(ctx context.Context) (CollectAIPermission, error) {
	objects := map[string]localObject{}
	if err := s.engine.collectAIPermission(ctx, objects); err != nil {
		return CollectAIPermission{}, err
	}
	object, found := objects["global"]
	if !found {
		return CollectAIPermission{}, nil
	}
	var payload aiPermissionObject
	if err := json.Unmarshal(object.plaintext, &payload); err != nil {
		return CollectAIPermission{}, err
	}
	return CollectAIPermission{ID: payload.ID, Mode: payload.Mode, DangerRules: payload.DangerRules, UpdatedAt: payload.UpdatedAt, Found: true}, nil
}

func (s *Service) CollectPreferences(ctx context.Context) ([]CollectPreference, error) {
	userID, err := s.preferenceUserID(ctx)
	if err != nil {
		return nil, err
	}
	objects := map[string]localObject{}
	if err := s.engine.collectPreferences(ctx, userID, objects); err != nil {
		return nil, err
	}
	out := make([]CollectPreference, 0, len(objects))
	for _, object := range objects {
		var payload preferenceObject
		if err := json.Unmarshal(object.plaintext, &payload); err != nil {
			return nil, err
		}
		out = append(out, CollectPreference{ID: payload.ID, Key: payload.Key, Value: payload.Value, UpdatedAt: payload.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
