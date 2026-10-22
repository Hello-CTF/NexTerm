package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	InventorySourceSSHConfig     = "ssh-config"
	InventorySourceDockerContext = "docker-context"
	InventorySourceKubernetes    = "kubernetes"
)

const inventoryOptionsKey = "inventory"

type InventoryItem struct {
	Key      string
	Kind     string
	Name     string
	Host     *string
	Port     *int32
	Username *string
	AuthKind *string
	KeyPath  *string
	CredID   *string
	Options  map[string]any
}

type InventorySyncResult struct {
	GroupID   string `json:"groupId"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Removed   int    `json:"removed"`
	Unchanged int    `json:"unchanged"`
}

type inventoryMetadata struct {
	Source  string   `json:"source"`
	Key     string   `json:"key"`
	Options []string `json:"options,omitempty"`
}

var inventorySyncMu sync.Mutex

func InventoryDefaultGroupName(source string) string {
	switch source {
	case InventorySourceSSHConfig:
		return "ssh-config"
	case InventorySourceDockerContext:
		return "docker-contexts"
	case InventorySourceKubernetes:
		return "kubernetes"
	default:
		return ""
	}
}

func validateInventorySource(source string) error {
	if InventoryDefaultGroupName(source) == "" {
		return badParam(fmt.Errorf("未知的清单来源 %q", source))
	}
	return nil
}

func validateInventoryCreateOptions(raw string) error {
	var options map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil
	}
	if _, exists := options[inventoryOptionsKey]; exists {
		return badParam(errString("inventory 是清单源保留选项，手工资产不能伪造来源标记"))
	}
	return nil
}

func decodeInventoryOptions(raw string) (map[string]any, error) {
	options := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return options, nil
	}
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil, err
	}
	if options == nil {
		options = map[string]any{}
	}
	return options, nil
}

func inventoryMetaFromOptions(options map[string]any) (inventoryMetadata, bool, error) {
	raw, exists := options[inventoryOptionsKey]
	if !exists {
		return inventoryMetadata{}, false, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return inventoryMetadata{}, false, err
	}
	var meta inventoryMetadata
	if err := json.Unmarshal(encoded, &meta); err != nil {
		return inventoryMetadata{}, false, err
	}
	if strings.TrimSpace(meta.Source) == "" || strings.TrimSpace(meta.Key) == "" {
		return inventoryMetadata{}, false, errors.New("inventory source and key are required")
	}
	seen := map[string]bool{}
	for _, key := range meta.Options {
		if key == "" || key == inventoryOptionsKey || seen[key] {
			return inventoryMetadata{}, false, errors.New("inventory source-owned option keys are invalid")
		}
		seen[key] = true
	}
	return meta, true, nil
}

func inventoryReadOnly(meta inventoryMetadata, field string) error {
	return badParam(fmt.Errorf("资产由清单源 %s 管理，%s 为只读字段", meta.Source, field))
}

func inventoryOptionalChanged[T comparable](current *T, patch Optional[T]) bool {
	if !patch.Set {
		return false
	}
	if current == nil || patch.Value == nil {
		return current != nil || patch.Value != nil
	}
	return *current != *patch.Value
}

func validateInventoryPatch(current AssetRow, patch AssetPatch) error {
	currentOptions, err := decodeInventoryOptions(current.OptionsJSON)
	if err != nil {
		if patch.OptionsJSON != nil {
			return badParam(errString("资产现有高级选项不是有效 JSON"))
		}
		return nil
	}
	meta, managed, err := inventoryMetaFromOptions(currentOptions)
	if err != nil {
		return badParam(errString("资产清单源标记无效"))
	}
	if !managed {
		if patch.OptionsJSON != nil {
			return validateInventoryCreateOptions(*patch.OptionsJSON)
		}
		return nil
	}
	for _, field := range []struct {
		name    string
		changed bool
	}{
		{name: "分组", changed: inventoryOptionalChanged(current.GroupID, patch.GroupID)},
		{name: "名称", changed: patch.Name != nil && strings.TrimSpace(*patch.Name) != current.Name},
		{name: "主机", changed: inventoryOptionalChanged(current.Host, patch.Host)},
		{name: "端口", changed: inventoryOptionalChanged(current.Port, patch.Port)},
		{name: "用户名", changed: inventoryOptionalChanged(current.Username, patch.Username)},
		{name: "认证方式", changed: inventoryOptionalChanged(current.AuthKind, patch.AuthKind)},
		{name: "私钥路径", changed: inventoryOptionalChanged(current.KeyPath, patch.KeyPath)},
		{name: "凭据引用", changed: inventoryOptionalChanged(current.CredID, patch.CredID)},
	} {
		if field.changed {
			return inventoryReadOnly(meta, field.name)
		}
	}
	if patch.OptionsJSON == nil {
		return nil
	}
	newOptions, err := decodeInventoryOptions(*patch.OptionsJSON)
	if err != nil {
		return badParam(errString("高级选项必须是有效的 JSON 对象"))
	}
	if !reflect.DeepEqual(currentOptions[inventoryOptionsKey], newOptions[inventoryOptionsKey]) {
		return inventoryReadOnly(meta, "清单源标记")
	}
	for _, key := range meta.Options {
		currentValue, currentExists := currentOptions[key]
		newValue, newExists := newOptions[key]
		if currentExists != newExists || !reflect.DeepEqual(currentValue, newValue) {
			return inventoryReadOnly(meta, "来源选项 "+key)
		}
	}
	return nil
}

func inventoryOptional[T any](value *T) Optional[T] {
	if value == nil {
		return Null[T]()
	}
	return Value(*value)
}

func inventoryOptionsJSON(current *AssetRow, source string, item InventoryItem) (string, error) {
	options := map[string]any{}
	var ownedOptionKeys []string
	if current != nil {
		decoded, err := decodeInventoryOptions(current.OptionsJSON)
		if err != nil {
			return "", badParam(errString("资产现有高级选项不是有效 JSON"))
		}
		meta, managed, err := inventoryMetaFromOptions(decoded)
		if err != nil || !managed {
			return "", badParam(errString("资产清单源标记无效"))
		}
		for key, value := range decoded {
			if key != inventoryOptionsKey {
				options[key] = value
			}
		}
		for _, key := range meta.Options {
			delete(options, key)
		}
	}
	for key, value := range item.Options {
		if key == inventoryOptionsKey {
			continue
		}
		options[key] = value
		ownedOptionKeys = append(ownedOptionKeys, key)
	}
	sort.Strings(ownedOptionKeys)
	options[inventoryOptionsKey] = inventoryMetadata{Source: source, Key: item.Key, Options: ownedOptionKeys}
	encoded, err := json.Marshal(options)
	if err != nil {
		return "", badParam(fmt.Errorf("清单来源选项无法编码: %w", err))
	}
	return string(encoded), nil
}

func inventoryOptionsForSource(current *AssetRow, source string, item InventoryItem) (string, error) {
	return inventoryOptionsJSON(current, source, item)
}

func inventoryJSONEqual(left, right string) (bool, error) {
	var leftValue, rightValue any
	if err := json.Unmarshal([]byte(left), &leftValue); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(right), &rightValue); err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftValue, rightValue), nil
}

func inventoryAssetChanged(current AssetRow, patch AssetPatch, optionsJSON string) (bool, error) {
	if inventoryOptionalChanged(current.GroupID, patch.GroupID) ||
		inventoryOptionalChanged(current.Host, patch.Host) ||
		inventoryOptionalChanged(current.Port, patch.Port) ||
		inventoryOptionalChanged(current.Username, patch.Username) ||
		inventoryOptionalChanged(current.AuthKind, patch.AuthKind) ||
		inventoryOptionalChanged(current.KeyPath, patch.KeyPath) ||
		inventoryOptionalChanged(current.CredID, patch.CredID) ||
		patch.Name != nil && strings.TrimSpace(*patch.Name) != current.Name {
		return true, nil
	}
	equal, err := inventoryJSONEqual(current.OptionsJSON, optionsJSON)
	return !equal, err
}

func (s *Store) ensureInventoryGroup(ctx context.Context, source, groupID string) (string, error) {
	if strings.TrimSpace(groupID) != "" {
		group, err := s.GroupGet(ctx, strings.TrimSpace(groupID))
		if err != nil {
			return "", err
		}
		return group.ID, nil
	}
	name := InventoryDefaultGroupName(source)
	groups, err := s.GroupList(ctx)
	if err != nil {
		return "", err
	}
	for _, group := range groups {
		if group.ParentID == nil && group.Name == name {
			return group.ID, nil
		}
	}
	group, err := s.GroupCreate(ctx, GroupInput{Name: name})
	if err != nil {
		return "", err
	}
	return group.ID, nil
}

func (s *Store) InventorySync(ctx context.Context, source, groupID string, items []InventoryItem) (InventorySyncResult, error) {
	inventorySyncMu.Lock()
	defer inventorySyncMu.Unlock()
	var result InventorySyncResult
	if err := validateInventorySource(source); err != nil {
		return result, err
	}
	resolvedGroupID, err := s.ensureInventoryGroup(ctx, source, groupID)
	if err != nil {
		return result, err
	}
	result.GroupID = resolvedGroupID
	desired := make(map[string]InventoryItem, len(items))
	for _, item := range items {
		item.Key = strings.TrimSpace(item.Key)
		item.Name = strings.TrimSpace(item.Name)
		if item.Key == "" || item.Name == "" || item.Kind == "" {
			return InventorySyncResult{}, badParam(errString("清单项必须包含 key、类型和名称"))
		}
		if _, exists := desired[item.Key]; exists {
			return InventorySyncResult{}, badParam(fmt.Errorf("清单项 key %q 重复", item.Key))
		}
		desired[item.Key] = item
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		asset := query.Use(tx).AssetRow
		rows := []AssetRow{}
		if err := asset.WithContext(ctx).
			Where(ownerConditions(ctx, asset.TableName())...).
			Order(asset.Sort, asset.Name).
			Scan(&rows); err != nil {
			return dbError(err)
		}
		liveByKey := map[string]AssetRow{}
		managed := make([]AssetRow, 0)
		for _, row := range rows {
			if row.DeletedAt != nil {
				continue
			}
			options, err := decodeInventoryOptions(row.OptionsJSON)
			if err != nil {
				continue
			}
			meta, ok, err := inventoryMetaFromOptions(options)
			if err != nil {
				return badParam(fmt.Errorf("资产 %s 的清单源标记无效: %w", row.ID, err))
			}
			if !ok || meta.Source != source {
				continue
			}
			managed = append(managed, row)
			if _, exists := liveByKey[meta.Key]; exists {
				return badParam(fmt.Errorf("清单源 %s 中存在重复资产键 %q，请先清理重复资产", source, meta.Key))
			}
			liveByKey[meta.Key] = row
		}
		for key, item := range desired {
			current, exists := liveByKey[key]
			if exists && current.Kind == item.Kind {
				optionsJSON, err := inventoryOptionsForSource(&current, source, item)
				if err != nil {
					return err
				}
				patch := AssetPatch{
					GroupID: Value(resolvedGroupID), Name: &item.Name,
					Host: inventoryOptional(item.Host), Port: inventoryOptional(item.Port), Username: inventoryOptional(item.Username),
					AuthKind: inventoryOptional(item.AuthKind), KeyPath: inventoryOptional(item.KeyPath), CredID: inventoryOptional(item.CredID),
					OptionsJSON: &optionsJSON,
				}
				changed, err := inventoryAssetChanged(current, patch, optionsJSON)
				if err != nil {
					return badParam(errString("资产现有高级选项不是有效 JSON"))
				}
				if !changed {
					result.Unchanged++
					continue
				}
				if _, err := s.assetUpdateCAS(ctx, tx, current.ID, current.UpdatedAt, patch, true); err != nil {
					return err
				}
				result.Updated++
				continue
			}
			if exists {
				if err := s.assetDeleteCAS(ctx, tx, current, current.UpdatedAt); err != nil {
					return err
				}
				result.Removed++
			}
			optionsJSON, err := inventoryOptionsForSource(nil, source, item)
			if err != nil {
				return err
			}
			if _, err := s.assetCreate(ctx, tx, AssetInput{
				GroupID: &resolvedGroupID, Kind: item.Kind, Name: item.Name,
				Host: item.Host, Port: item.Port, Username: item.Username, AuthKind: item.AuthKind, KeyPath: item.KeyPath, CredID: item.CredID,
				OptionsJSON: optionsJSON,
			}); err != nil {
				return err
			}
			result.Created++
		}
		for _, row := range managed {
			if _, exists := desired[inventoryKeyForRow(row)]; exists {
				continue
			}
			if err := s.assetDeleteCAS(ctx, tx, row, row.UpdatedAt); err != nil {
				return err
			}
			result.Removed++
		}
		return nil
	})
	if err != nil {
		return InventorySyncResult{}, err
	}
	return result, nil
}

func inventoryKeyForRow(row AssetRow) string {
	options, err := decodeInventoryOptions(row.OptionsJSON)
	if err != nil {
		return ""
	}
	meta, ok, err := inventoryMetaFromOptions(options)
	if err != nil || !ok {
		return ""
	}
	return meta.Key
}

func (s *Store) assetDeleteCAS(ctx context.Context, db *gorm.DB, row AssetRow, expectedUpdatedAt int64) error {
	if row.Builtin {
		return badParam(errString("内置资产不能由清单源删除"))
	}
	now := nextWriteVersion(expectedUpdatedAt)
	asset := query.Use(db).AssetRow
	info, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(row.ID), asset.UpdatedAt.Eq(expectedUpdatedAt), asset.DeletedAt.IsNull()).
		Where(ownerConditions(ctx, asset.TableName())...).
		UpdateColumns(map[string]any{"deleted_at": now, "updated_at": now})
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected != 1 {
		return ErrWriteConflict
	}
	return nil
}
