package sync

import (
	"context"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type assetAcceptance uint8

const (
	assetAccepted assetAcceptance = iota
	assetSkippedNewer
	assetRefused
)

type assetDecision struct {
	acceptance assetAcceptance
	warning    string
}

func effectiveRevision(updatedAt int64, deletedAt *int64) int64 {
	if deletedAt != nil && *deletedAt > updatedAt {
		return *deletedAt
	}
	return updatedAt
}

func (s *Service) planAssets(ctx context.Context, assets []AssetPayload, force bool) []assetDecision {
	decisions := make([]assetDecision, len(assets))
	for i, asset := range assets {
		decision := assetDecision{acceptance: assetAccepted}
		local, err := s.store.AssetGet(ctx, asset.ID)
		exists := err == nil
		switch {
		case err != nil && !isNotFound(err):
			decision.acceptance = assetRefused
			decision.warning = fmt.Sprintf("无法检查资产 %s: %v", asset.ID, err)
		case asset.ID == store.BuiltinLocalAssetID || (exists && local.Builtin):
			decision.acceptance = assetRefused
			decision.warning = "内置「当前设备」不接受同步覆盖"
		case store.EnsureID(asset.ID) != nil || strings.TrimSpace(asset.Name) == "":
			decision.acceptance = assetRefused
			decision.warning = fmt.Sprintf("资产 %s 的 ID 或名称不合法，已拒绝导入", asset.ID)
		case exists && !force && effectiveRevision(local.UpdatedAt, local.DeletedAt) > effectiveRevision(asset.UpdatedAt, asset.DeletedAt):
			decision.acceptance = assetSkippedNewer
			decision.warning = fmt.Sprintf("资产 %s 的本机版本较新，已跳过；如需覆盖请使用强制同步", asset.ID)
		}
		decisions[i] = decision
	}
	return decisions
}

func blockedCredentials(assets []AssetPayload, decisions []assetDecision) map[string]bool {
	blocked := map[string]bool{}
	for i, asset := range assets {
		if asset.CredID == nil || strings.TrimSpace(*asset.CredID) == "" || decisions[i].acceptance == assetAccepted {
			continue
		}
		blocked[*asset.CredID] = true
	}
	return blocked
}
