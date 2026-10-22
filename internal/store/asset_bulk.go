package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const AssetBulkEditMaxTargets = 64

type AssetBulkEditTarget struct {
	AssetID           string `json:"assetId"`
	ExpectedUpdatedAt int64  `json:"expectedUpdatedAt"`
}

type AssetBulkEditPatch struct {
	GroupID      Optional[string]
	Tags         *string
	JumpAssetID  Optional[string]
	CredentialID Optional[string]
	Port         Optional[int32]
}

type AssetBulkEditResult struct {
	AssetID  string
	Asset    *AssetRow
	Conflict bool
	Err      error
}

func (p AssetBulkEditPatch) empty() bool {
	return !p.GroupID.Set && p.Tags == nil && !p.JumpAssetID.Set && !p.CredentialID.Set && !p.Port.Set
}

func (s *Store) AssetBulkEdit(ctx context.Context, targets []AssetBulkEditTarget, patch AssetBulkEditPatch) ([]AssetBulkEditResult, error) {
	if len(targets) == 0 || len(targets) > AssetBulkEditMaxTargets {
		return nil, badParam(errString("批量编辑目标数必须在 1-64 之间"))
	}
	if patch.empty() {
		return nil, badParam(errString("批量编辑至少需要一个修改字段"))
	}
	if patch.Port.Set && patch.Port.Value != nil && (*patch.Port.Value < 1 || *patch.Port.Value > 65535) {
		return nil, badParam(errString("端口需在 1-65535 之间"))
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.AssetID) == "" {
			return nil, badParam(errString("批量编辑目标缺少资产 ID"))
		}
		if _, exists := seen[target.AssetID]; exists {
			return nil, badParam(errString("批量编辑目标不能重复"))
		}
		seen[target.AssetID] = struct{}{}
	}
	if patch.CredentialID.Set && patch.CredentialID.Value != nil {
		if strings.TrimSpace(*patch.CredentialID.Value) == "" {
			return nil, badParam(errString("凭据 ID 不能为空"))
		}
		if _, err := s.CredentialGetRow(ctx, *patch.CredentialID.Value); err != nil {
			return nil, err
		}
	}
	if patch.JumpAssetID.Set && patch.JumpAssetID.Value != nil {
		if strings.TrimSpace(*patch.JumpAssetID.Value) == "" {
			return nil, badParam(errString("跳板机资产 ID 不能为空"))
		}
		jump, err := s.AssetGetLive(ctx, *patch.JumpAssetID.Value)
		if err != nil {
			return nil, err
		}
		if jump.Kind != "ssh" && jump.Kind != "docker" {
			return nil, badParam(errString("跳板机必须是 SSH 或 Docker 资产"))
		}
	}

	results := make([]AssetBulkEditResult, len(targets))
	for index, target := range targets {
		results[index] = s.assetBulkEditTarget(ctx, target, patch)
	}
	return results, nil
}

func (s *Store) assetBulkEditTarget(ctx context.Context, target AssetBulkEditTarget, patch AssetBulkEditPatch) AssetBulkEditResult {
	result := AssetBulkEditResult{AssetID: target.AssetID}
	current, err := s.AssetGetLive(ctx, target.AssetID)
	if err != nil {
		result.Err = err
		return result
	}
	if current.UpdatedAt != target.ExpectedUpdatedAt {
		result.Conflict = true
		result.Err = ErrWriteConflict
		return result
	}
	if patch.JumpAssetID.Set && patch.JumpAssetID.Value != nil && *patch.JumpAssetID.Value == current.ID {
		result.Err = badParam(errString("资产不能把自身设为跳板机"))
		return result
	}
	var optionsJSON *string
	if patch.JumpAssetID.Set {
		options := map[string]any{}
		if strings.TrimSpace(current.OptionsJSON) != "" {
			if err := json.Unmarshal([]byte(current.OptionsJSON), &options); err != nil {
				result.Err = badParam(errString("资产现有高级选项不是有效 JSON"))
				return result
			}
		}
		if options == nil {
			options = map[string]any{}
		}
		if patch.JumpAssetID.Value == nil {
			delete(options, "jumpAssetId")
		} else {
			options["jumpAssetId"] = *patch.JumpAssetID.Value
		}
		encoded, err := json.Marshal(options)
		if err != nil {
			result.Err = err
			return result
		}
		value := string(encoded)
		optionsJSON = &value
	}
	updated, err := s.AssetUpdateCAS(ctx, s.db, current.ID, target.ExpectedUpdatedAt, AssetPatch{
		GroupID: patch.GroupID, Tags: patch.Tags, CredID: patch.CredentialID, Port: patch.Port, OptionsJSON: optionsJSON,
	})
	if err != nil {
		result.Conflict = errors.Is(err, ErrWriteConflict)
		result.Err = err
		return result
	}
	result.Asset = &updated
	return result
}
