package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const preferenceObjectPrefix = "preference:"

// aiGrantFutureTolerance bounds how far ahead of the local clock a remote grant revision may be.
const aiGrantFutureTolerance = 5 * time.Minute

// aiGrantObjectID prefixes split the ai_grant kind into its three entity families (see object.go).
const (
	aiGrantDevicePrefix = "device/"
	aiGrantRulePrefix   = "rule/"
	aiGrantModePrefix   = "mode/"
)

func (e *Engine) collectAIPermission(ctx context.Context, objects map[string]localObject) error {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return err
	}
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
	rows, err := store.SettingRowsWithPrefix(ctx, e.store.GORM(), prefix)
	if err != nil {
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

// settingRow reads one settings row with its revision (updated_at); found=false means the row does not exist.
func (e *Engine) settingRow(ctx context.Context, key string) (string, int64, bool, error) {
	setting := query.Use(e.store.GORM()).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(key)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return row.Value, row.UpdatedAt, true, nil
}

func aiGrantModeValid(mode guard.Mode) bool {
	return mode == guard.ReadOnly || mode == guard.ReadWrite || mode == guard.Silent || mode == guard.Unattended
}

func aiGrantKindStrings(kinds []guard.GrantKind) []string {
	values := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		values = append(values, string(kind))
	}
	return values
}

// collectAIGrantObjects emits one object per device grant, grant rule, and per-device permission mode;
// grants and rules carry per-entry revisions, modes share the mode map row's revision as their LWW clock (same tradeoff as ai_profiles).
// Grants, rules, and modes use the request owner's settings keys; the sync object IDs and payload shapes stay unchanged.
// Rejected pre-1.3.0 grant records are never pushed: they are reported on the round (report is nil on the browser
// collect path, where the grant panel lists them as legacy) instead of propagating unmigrated data to other devices.
func (e *Engine) collectAIGrantObjects(ctx context.Context, objects map[string]localObject, report *SyncReport) error {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return err
	}
	value, _, found, err := e.settingRow(ctx, guard.DeviceGrantsSettingKeyForUser(ctx))
	if err != nil {
		return err
	}
	if found && value != "" {
		grants, err := guard.DecodeDeviceGrants(value)
		if err != nil {
			return ipc.WrapError(ipc.CodeBadParam, "AI 设备授权数据损坏", err)
		}
		if legacy := grants.LegacyIDs(); len(legacy) > 0 && report != nil {
			report.warnf("%d 条 AI 设备授权为 1.3.0 之前的旧格式，在这台设备上不生效，也不会从这台设备推送（其他设备上的同一授权可能仍然有效），请在 AI 授权设置中重新授权或清除：%s", len(legacy), strings.Join(legacy, ", "))
		}
		for _, grant := range grants.Current {
			id := aiGrantDevicePrefix + grant.DeviceID
			payload, err := marshalObject(aiGrantDeviceObject{ID: id, DeviceID: grant.DeviceID, Kinds: aiGrantKindStrings(grant.Kinds), UpdatedAt: grant.UpdatedAt})
			if err != nil {
				return err
			}
			objects[id] = localObject{KindAIGrant, payload}
		}
	}
	value, _, found, err = e.settingRow(ctx, guard.GrantRulesSettingKeyForUser(ctx))
	if err != nil {
		return err
	}
	if found && value != "" {
		rules, err := guard.DecodeGrantRules(value)
		if err != nil {
			return ipc.WrapError(ipc.CodeBadParam, "AI 授权规则数据损坏", err)
		}
		if legacy := rules.LegacyIDs(); len(legacy) > 0 && report != nil {
			report.warnf("%d 条 AI 授权规则为 1.3.0 之前的旧格式，在这台设备上不生效，也不会从这台设备推送（其他设备上的同一规则可能仍然有效），请在 AI 授权设置中重新添加或清除：%s", len(legacy), strings.Join(legacy, ", "))
		}
		for _, rule := range rules.Current {
			id := aiGrantRulePrefix + rule.ID
			payload, err := marshalObject(aiGrantRuleObject{
				ID: id, DeviceID: rule.DeviceID, Action: rule.Action, Path: rule.Path,
				ExpiresAt: rule.ExpiresAt, CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
			})
			if err != nil {
				return err
			}
			objects[id] = localObject{KindAIGrant, payload}
		}
	}
	value, revision, found, err := e.settingRow(ctx, guard.DevicePermissionSettingKeyForUser(ctx))
	if err != nil {
		return err
	}
	if found && value != "" {
		var modes map[string]guard.Mode
		if err := json.Unmarshal([]byte(value), &modes); err != nil {
			return ipc.WrapError(ipc.CodeBadParam, "AI 设备权限模式数据损坏", err)
		}
		for deviceID, mode := range modes {
			if !aiGrantModeValid(mode) {
				continue
			}
			id := aiGrantModePrefix + deviceID
			payload, err := marshalObject(aiGrantModeObject{ID: id, DeviceID: deviceID, Mode: string(mode), UpdatedAt: revision})
			if err != nil {
				return err
			}
			objects[id] = localObject{KindAIGrant, payload}
		}
	}
	return nil
}

// aiGrantSettingPut writes a guard settings row back as a whole, preserving the given revision verbatim (same semantics as settingPut for ai_permission).
// Device grant and rule sets go through the guard codec so current records keep their generations and rejected legacy
// records are written back verbatim.
func (e *Engine) aiGrantSettingPut(ctx context.Context, key string, value any, updatedAt int64) error {
	if _, err := e.aiOwnerID(ctx); err != nil {
		return err
	}
	var encoded string
	switch set := value.(type) {
	case guard.DeviceGrantSet:
		raw, err := set.Encode()
		if err != nil {
			return err
		}
		encoded = raw
	case guard.GrantRuleSet:
		raw, err := set.Encode()
		if err != nil {
			return err
		}
		encoded = raw
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	return e.settingPut(ctx, key, encoded, updatedAt)
}

func (e *Engine) applyAIPermissionObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 权限应用需要账号身份: %v", err)
		return false, false
	}
	var payload aiPermissionObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 权限对象数据损坏：%v", err)
		return false, false
	}
	if payload.ID != "global" {
		report.warnf("AI 权限对象 ID %s 不受支持", payload.ID)
		return false, false
	}
	config := guard.Config{Mode: guard.Mode(payload.Mode), DangerRules: payload.DangerRules}.Normalized()
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查 AI 权限删除标记：%v", err)
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
		report.warnf("AI 权限删除标记清理失败：%v", err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIGrantObject(ctx context.Context, objectID string, plaintext []byte, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 授权应用需要账号身份: %v", err)
		return false, false
	}
	// A remote revision far in the future would outrank every later revocation tombstone (LWW), so such
	// objects are refused until the clock catches up.
	var clock struct {
		UpdatedAt int64 `json:"updatedAt"`
	}
	if err := unmarshalObject(plaintext, &clock); err == nil && clock.UpdatedAt > time.Now().Add(aiGrantFutureTolerance).UnixMilli() {
		report.warnf("AI 授权对象 %s 的时间戳超前本机时钟，已拒绝", objectID)
		return false, false
	}
	switch {
	case strings.HasPrefix(objectID, aiGrantDevicePrefix):
		return e.applyAIGrantDevice(ctx, plaintext, report)
	case strings.HasPrefix(objectID, aiGrantRulePrefix):
		return e.applyAIGrantRule(ctx, plaintext, report)
	case strings.HasPrefix(objectID, aiGrantModePrefix):
		return e.applyAIGrantMode(ctx, plaintext, report)
	default:
		report.warnf("AI 授权对象 ID %s 不受支持", objectID)
		return false, false
	}
}

func (e *Engine) applyAIGrantDevice(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 授权应用需要账号身份: %v", err)
		return false, false
	}
	var payload aiGrantDeviceObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 授权对象数据损坏：%v", err)
		return false, false
	}
	// DeviceID is normalized exactly like guard.Grant trims it locally; the object ID must match the normalized form,
	// keeping LWW and tombstone identity (recorded by the guard trimmed) consistent.
	payload.DeviceID = strings.TrimSpace(payload.DeviceID)
	if payload.ID != aiGrantDevicePrefix+payload.DeviceID || payload.DeviceID == "" {
		report.warnf("AI 授权对象 ID %s 不受支持", payload.ID)
		return false, false
	}
	kinds, err := guard.ParseGrantKinds(payload.Kinds)
	if err != nil {
		report.warnf("AI 授权对象 %s 种类无效: %v", payload.ID, err)
		return false, false
	}
	if err := e.validateAIGrantTarget(ctx, payload.DeviceID); err != nil {
		report.warnf("AI 授权目标校验失败: %v", err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查 AI 授权 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	value, revision, _, err := e.settingRow(ctx, guard.DeviceGrantsSettingKeyForUser(ctx))
	if err != nil {
		report.warnf("无法读取 AI 设备授权: %v", err)
		return false, false
	}
	grants, err := guard.DecodeDeviceGrants(value)
	if err != nil {
		report.warnf("AI 设备授权数据损坏: %v", err)
		return false, false
	}
	local, exists := grants.Current[payload.DeviceID]
	if exists {
		localPlaintext, err := marshalObject(aiGrantDeviceObject{ID: payload.ID, DeviceID: local.DeviceID, Kinds: aiGrantKindStrings(local.Kinds), UpdatedAt: local.UpdatedAt})
		if err != nil {
			return false, false
		}
		if bytes.Equal(plaintext, localPlaintext) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPlaintext) {
			return false, false
		}
	}
	// A device grant in the guard store is a long-term authorization, so permanence is bound
	// explicitly and a fresh generation is issued exactly like guard.Grant writes it locally.
	// A rejected pre-1.3.0 local record for the same device is replaced by the synced state;
	// bystander records keep their generations, and other legacy records stay verbatim.
	grants.Current[payload.DeviceID] = guard.DeviceGrant{DeviceID: payload.DeviceID, Kinds: kinds, Permanent: true, UpdatedAt: payload.UpdatedAt}.Renewed()
	delete(grants.Legacy, payload.DeviceID)
	if err := e.aiGrantSettingPut(ctx, guard.DeviceGrantsSettingKeyForUser(ctx), grants, max(revision, payload.UpdatedAt)); err != nil {
		report.warnf("AI 设备授权应用失败: %v", err)
		return false, false
	}
	if err := e.clearTombstone(ctx, payload.ID); err != nil {
		report.warnf("AI 授权删除标记清理失败：%v", err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIGrantRule(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 授权规则应用需要账号身份: %v", err)
		return false, false
	}
	var payload aiGrantRuleObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 授权规则对象数据损坏：%v", err)
		return false, false
	}
	ruleID := strings.TrimPrefix(payload.ID, aiGrantRulePrefix)
	// DeviceID and Path are normalized exactly like guard.AddRule trims them locally; the object ID must match
	// the normalized form, keeping LWW and tombstone identity (recorded by the guard trimmed) consistent.
	payload.DeviceID = strings.TrimSpace(payload.DeviceID)
	payload.Path = strings.TrimSpace(payload.Path)
	if ruleID == "" || ruleID == payload.ID || payload.DeviceID == "" {
		report.warnf("AI 授权规则对象 ID %s 不受支持", payload.ID)
		return false, false
	}
	if !guard.RuleActionSupported(payload.Action) {
		report.warnf("AI 授权规则 %s 动作不受支持: %s", payload.ID, payload.Action)
		return false, false
	}
	if guard.RuleActionNeedsPath(payload.Action) && payload.Path == "" {
		report.warnf("AI 授权规则 %s 缺少路径范围", payload.ID)
		return false, false
	}
	if !guard.RuleActionNeedsPath(payload.Action) && payload.Path != "" {
		report.warnf("AI 授权规则 %s 的路径范围不受支持", payload.ID)
		return false, false
	}
	if err := e.validateAIGrantTarget(ctx, payload.DeviceID); err != nil {
		report.warnf("AI 授权目标校验失败: %v", err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查 AI 授权规则 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	value, revision, _, err := e.settingRow(ctx, guard.GrantRulesSettingKeyForUser(ctx))
	if err != nil {
		report.warnf("无法读取 AI 授权规则: %v", err)
		return false, false
	}
	rules, err := guard.DecodeGrantRules(value)
	if err != nil {
		report.warnf("AI 授权规则数据损坏: %v", err)
		return false, false
	}
	applied := guard.GrantRule{
		ID: ruleID, DeviceID: payload.DeviceID, Action: payload.Action, Path: payload.Path,
		ExpiresAt: payload.ExpiresAt, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt,
	}.Renewed()
	index := -1
	for i, rule := range rules.Current {
		if rule.ID == ruleID {
			index = i
			break
		}
	}
	if index >= 0 {
		local := rules.Current[index]
		localPlaintext, err := marshalObject(aiGrantRuleObject{
			ID: payload.ID, DeviceID: local.DeviceID, Action: local.Action, Path: local.Path,
			ExpiresAt: local.ExpiresAt, CreatedAt: local.CreatedAt, UpdatedAt: local.UpdatedAt,
		})
		if err != nil {
			return false, false
		}
		if bytes.Equal(plaintext, localPlaintext) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPlaintext) {
			return false, false
		}
		rules.Current[index] = applied
	} else {
		rules.Current = append(rules.Current, applied)
	}
	// A rejected pre-1.3.0 local rule with the same ID is replaced by the synced state.
	for i, legacy := range rules.Legacy {
		if legacy.View.ID == ruleID {
			rules.Legacy = append(rules.Legacy[:i], rules.Legacy[i+1:]...)
			break
		}
	}
	if err := e.aiGrantSettingPut(ctx, guard.GrantRulesSettingKeyForUser(ctx), rules, max(revision, payload.UpdatedAt)); err != nil {
		report.warnf("AI 授权规则应用失败: %v", err)
		return false, false
	}
	if err := e.clearTombstone(ctx, payload.ID); err != nil {
		report.warnf("AI 授权规则删除标记清理失败：%v", err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIGrantMode(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 设备权限模式应用需要账号身份: %v", err)
		return false, false
	}
	var payload aiGrantModeObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 设备权限模式对象数据损坏：%v", err)
		return false, false
	}
	// DeviceID is normalized exactly like guard.SetDeviceMode trims it locally; the object ID must match the normalized form,
	// keeping LWW and tombstone identity (recorded by the guard trimmed) consistent.
	payload.DeviceID = strings.TrimSpace(payload.DeviceID)
	if payload.ID != aiGrantModePrefix+payload.DeviceID || payload.DeviceID == "" {
		report.warnf("AI 设备权限模式对象 ID %s 不受支持", payload.ID)
		return false, false
	}
	if !aiGrantModeValid(guard.Mode(payload.Mode)) {
		report.warnf("AI 设备权限模式 %s 不受支持: %s", payload.ID, payload.Mode)
		return false, false
	}
	if err := e.validateAIGrantTarget(ctx, payload.DeviceID); err != nil {
		report.warnf("AI 授权目标校验失败: %v", err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查 AI 设备权限模式 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	key := guard.DevicePermissionSettingKeyForUser(ctx)
	value, revision, found, err := e.settingRow(ctx, key)
	if err != nil {
		report.warnf("无法读取 AI 设备权限模式: %v", err)
		return false, false
	}
	modes := map[string]guard.Mode{}
	if found && value != "" {
		if err := json.Unmarshal([]byte(value), &modes); err != nil {
			report.warnf("AI 设备权限模式数据损坏: %v", err)
			return false, false
		}
		if modes == nil {
			modes = map[string]guard.Mode{}
		}
	}
	if local, exists := modes[payload.DeviceID]; exists {
		localPlaintext, err := marshalObject(aiGrantModeObject{ID: payload.ID, DeviceID: payload.DeviceID, Mode: string(local), UpdatedAt: revision})
		if err != nil {
			return false, false
		}
		if bytes.Equal(plaintext, localPlaintext) {
			return false, true
		}
		// Mode entries share the map row's revision as their LWW clock (same tradeoff as ai_profiles): revisions only increase.
		if !remoteWins(payload.UpdatedAt, revision, plaintext, localPlaintext) {
			return false, false
		}
	}
	modes[payload.DeviceID] = guard.Mode(payload.Mode)
	if err := e.aiGrantSettingPut(ctx, key, modes, max(revision, payload.UpdatedAt)); err != nil {
		report.warnf("AI 设备权限模式应用失败: %v", err)
		return false, false
	}
	if err := e.clearTombstone(ctx, payload.ID); err != nil {
		report.warnf("AI 设备权限模式删除标记清理失败：%v", err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIGrantTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 授权删除标记需要账号身份: %v", err)
		return false, false
	}
	switch {
	case strings.HasPrefix(objectID, aiGrantDevicePrefix):
		return e.applyAIGrantDeviceTombstone(ctx, objectID, deletedAt, report)
	case strings.HasPrefix(objectID, aiGrantRulePrefix):
		return e.applyAIGrantRuleTombstone(ctx, objectID, deletedAt, report)
	case strings.HasPrefix(objectID, aiGrantModePrefix):
		return e.applyAIGrantModeTombstone(ctx, objectID, deletedAt, report)
	default:
		report.warnf("AI 授权删除标记 ID %s 不受支持", objectID)
		return false, false
	}
}

func (e *Engine) applyAIGrantDeviceTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 授权删除标记需要账号身份: %v", err)
		return false, false
	}
	if err := e.validateAIGrantTarget(ctx, strings.TrimPrefix(objectID, aiGrantDevicePrefix)); err != nil {
		report.warnf("AI 授权目标校验失败: %v", err)
		return false, false
	}
	value, revision, _, err := e.settingRow(ctx, guard.DeviceGrantsSettingKeyForUser(ctx))
	if err != nil {
		report.warnf("无法读取 AI 设备授权: %v", err)
		return false, false
	}
	grants, err := guard.DecodeDeviceGrants(value)
	if err != nil {
		report.warnf("AI 设备授权数据损坏: %v", err)
		return false, false
	}
	deviceID := strings.TrimPrefix(objectID, aiGrantDevicePrefix)
	local, exists := grants.Current[deviceID]
	if legacy, ok := grants.Legacy[deviceID]; ok && !exists {
		local, exists = legacy.View, true
	}
	if exists && local.UpdatedAt > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindAIGrant, deletedAt); err != nil {
		report.warnf("AI 授权删除标记记录失败：%v", err)
		return false, false
	}
	if exists {
		delete(grants.Current, deviceID)
		delete(grants.Legacy, deviceID)
		if err := e.aiGrantSettingPut(ctx, guard.DeviceGrantsSettingKeyForUser(ctx), grants, max(revision, deletedAt)); err != nil {
			report.warnf("应用 AI 设备授权的删除标记失败：%v", err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyAIGrantRuleTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 授权规则删除标记需要账号身份: %v", err)
		return false, false
	}
	value, revision, _, err := e.settingRow(ctx, guard.GrantRulesSettingKeyForUser(ctx))
	if err != nil {
		report.warnf("无法读取 AI 授权规则: %v", err)
		return false, false
	}
	rules, err := guard.DecodeGrantRules(value)
	if err != nil {
		report.warnf("AI 授权规则数据损坏: %v", err)
		return false, false
	}
	ruleID := strings.TrimPrefix(objectID, aiGrantRulePrefix)
	var local guard.GrantRule
	index, legacyIndex := -1, -1
	for i, rule := range rules.Current {
		if rule.ID == ruleID {
			index, local = i, rule
			break
		}
	}
	for i, legacy := range rules.Legacy {
		if legacy.View.ID == ruleID {
			legacyIndex = i
			if index < 0 {
				local = legacy.View
			}
			break
		}
	}
	exists := index >= 0 || legacyIndex >= 0
	if exists && local.UpdatedAt > deletedAt {
		return false, false
	}
	if exists {
		if err := e.validateAIGrantTarget(ctx, local.DeviceID); err != nil {
			report.warnf("AI 授权目标校验失败: %v", err)
			return false, false
		}
	}
	if err := e.syncTombstonePut(ctx, objectID, KindAIGrant, deletedAt); err != nil {
		report.warnf("AI 授权规则删除标记记录失败：%v", err)
		return false, false
	}
	if exists {
		if index >= 0 {
			rules.Current = append(rules.Current[:index], rules.Current[index+1:]...)
		}
		if legacyIndex >= 0 {
			rules.Legacy = append(rules.Legacy[:legacyIndex], rules.Legacy[legacyIndex+1:]...)
		}
		if err := e.aiGrantSettingPut(ctx, guard.GrantRulesSettingKeyForUser(ctx), rules, max(revision, deletedAt)); err != nil {
			report.warnf("应用 AI 授权规则的删除标记失败：%v", err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyAIGrantModeTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 设备权限模式删除标记需要账号身份: %v", err)
		return false, false
	}
	if err := e.validateAIGrantTarget(ctx, strings.TrimPrefix(objectID, aiGrantModePrefix)); err != nil {
		report.warnf("AI 授权目标校验失败: %v", err)
		return false, false
	}
	key := guard.DevicePermissionSettingKeyForUser(ctx)
	value, revision, found, err := e.settingRow(ctx, key)
	if err != nil {
		report.warnf("无法读取 AI 设备权限模式: %v", err)
		return false, false
	}
	modes := map[string]guard.Mode{}
	if found && value != "" {
		if err := json.Unmarshal([]byte(value), &modes); err != nil {
			report.warnf("AI 设备权限模式数据损坏: %v", err)
			return false, false
		}
	}
	deviceID := strings.TrimPrefix(objectID, aiGrantModePrefix)
	// Mode entries share the map row's revision as their LWW clock (same tradeoff as ai_profiles).
	if _, exists := modes[deviceID]; exists && revision > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindAIGrant, deletedAt); err != nil {
		report.warnf("AI 设备权限模式删除标记记录失败：%v", err)
		return false, false
	}
	if _, exists := modes[deviceID]; exists {
		delete(modes, deviceID)
		if err := e.aiGrantSettingPut(ctx, key, modes, max(revision, deletedAt)); err != nil {
			report.warnf("应用 AI 设备权限模式的删除标记失败：%v", err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyPreferenceObject(ctx context.Context, plaintext []byte, report *SyncReport, userID string) (bool, bool) {
	var payload preferenceObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("偏好对象数据损坏：%v", err)
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
		report.warnf("无法检查偏好 %s 的删除标记：%v", payload.Key, err)
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
		report.warnf("偏好 %s 的删除标记清理失败：%v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIPermissionTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 权限删除标记需要账号身份: %v", err)
		return false, false
	}
	if objectID != "global" {
		report.warnf("AI 权限删除标记 ID %s 不受支持", objectID)
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
		report.warnf("AI 权限删除标记记录失败：%v", err)
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
		report.warnf("偏好删除标记需要账号身份")
		return false, false
	}
	preferenceKey := strings.TrimPrefix(objectID, preferenceObjectPrefix)
	if objectID != preferenceObjectPrefix+preferenceKey || !account.PreferenceKeyDeclared(preferenceKey) {
		report.warnf("偏好删除标记 ID %s 不受支持", objectID)
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
		report.warnf("偏好 %s 的删除标记记录失败：%v", objectID, err)
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

// CollectAIGrant* carry the ai_grant payload fields one-to-one, so the browser can assemble payloads directly (same contract as CollectAIPermission).
type CollectAIGrantDevice struct {
	ID        string   `json:"id"`
	DeviceID  string   `json:"deviceId"`
	Kinds     []string `json:"kinds"`
	UpdatedAt int64    `json:"updatedAt"`
}

type CollectAIGrantRule struct {
	ID        string `json:"id"`
	DeviceID  string `json:"deviceId"`
	Action    string `json:"action"`
	Path      string `json:"path,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type CollectAIGrantMode struct {
	ID        string `json:"id"`
	DeviceID  string `json:"deviceId"`
	Mode      string `json:"mode"`
	UpdatedAt int64  `json:"updatedAt"`
}

type CollectAIGrants struct {
	Grants []CollectAIGrantDevice `json:"grants"`
	Rules  []CollectAIGrantRule   `json:"rules"`
	Modes  []CollectAIGrantMode   `json:"modes"`
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

// CollectAIGrants lists device grants, grant rules, and per-device permission modes for the browser collect path; no secrets are involved.
func (s *Service) CollectAIGrants(ctx context.Context) (CollectAIGrants, error) {
	objects := map[string]localObject{}
	if err := s.engine.collectAIGrantObjects(ctx, objects, nil); err != nil {
		return CollectAIGrants{}, err
	}
	result := CollectAIGrants{Grants: []CollectAIGrantDevice{}, Rules: []CollectAIGrantRule{}, Modes: []CollectAIGrantMode{}}
	for id, object := range objects {
		switch {
		case strings.HasPrefix(id, aiGrantDevicePrefix):
			var payload aiGrantDeviceObject
			if err := json.Unmarshal(object.plaintext, &payload); err != nil {
				return CollectAIGrants{}, err
			}
			result.Grants = append(result.Grants, CollectAIGrantDevice{ID: payload.ID, DeviceID: payload.DeviceID, Kinds: payload.Kinds, UpdatedAt: payload.UpdatedAt})
		case strings.HasPrefix(id, aiGrantRulePrefix):
			var payload aiGrantRuleObject
			if err := json.Unmarshal(object.plaintext, &payload); err != nil {
				return CollectAIGrants{}, err
			}
			result.Rules = append(result.Rules, CollectAIGrantRule{
				ID: payload.ID, DeviceID: payload.DeviceID, Action: payload.Action, Path: payload.Path,
				ExpiresAt: payload.ExpiresAt, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt,
			})
		case strings.HasPrefix(id, aiGrantModePrefix):
			var payload aiGrantModeObject
			if err := json.Unmarshal(object.plaintext, &payload); err != nil {
				return CollectAIGrants{}, err
			}
			result.Modes = append(result.Modes, CollectAIGrantMode{ID: payload.ID, DeviceID: payload.DeviceID, Mode: payload.Mode, UpdatedAt: payload.UpdatedAt})
		}
	}
	sort.Slice(result.Grants, func(i, j int) bool { return result.Grants[i].ID < result.Grants[j].ID })
	sort.Slice(result.Rules, func(i, j int) bool { return result.Rules[i].ID < result.Rules[j].ID })
	sort.Slice(result.Modes, func(i, j int) bool { return result.Modes[i].ID < result.Modes[j].ID })
	return result, nil
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
