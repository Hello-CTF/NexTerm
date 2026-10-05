package sync

import (
	"context"
	"fmt"
	"reflect"
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
	acceptance     assetAcceptance
	warning        string
	localRevision  int64
	remoteRevision int64
	equalRevision  bool
}

// effectiveRevision 返回记录的有效修订号。冲突解决假设各设备时钟已经 NTP 同步：
// 协议不检测也不校正时钟偏移，修订号较大者胜出；修订号相同时按 Origin 字典序裁决，
// 使双向同步无需额外协商即可收敛到同一结果。
func effectiveRevision(updatedAt int64, deletedAt *int64) int64 {
	if deletedAt != nil && *deletedAt > updatedAt {
		return *deletedAt
	}
	return updatedAt
}

func (s *Service) planAssets(ctx context.Context, assets []AssetPayload, force bool, bundleOrigin, localOrigin string) []assetDecision {
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
		case exists && !force:
			decision.planConflict(asset, local, reflect.DeepEqual(payloadFromAsset(local), asset), bundleOrigin, localOrigin)
		}
		decisions[i] = decision
	}
	return decisions
}

func (d *assetDecision) planConflict(asset AssetPayload, local store.AssetRow, contentEqual bool, bundleOrigin, localOrigin string) {
	d.localRevision = effectiveRevision(local.UpdatedAt, local.DeletedAt)
	d.remoteRevision = effectiveRevision(asset.UpdatedAt, asset.DeletedAt)
	switch {
	case d.localRevision > d.remoteRevision:
		d.acceptance = assetSkippedNewer
		d.warning = fmt.Sprintf("资产 %s 的本机版本较新，已跳过；如需覆盖请使用强制同步", asset.ID)
	case d.localRevision == d.remoteRevision && !contentEqual && bundleOrigin != localOrigin:
		d.equalRevision = true
		if bundleOrigin > localOrigin {
			d.warning = fmt.Sprintf("资产 %s 本机与远端修订号相同（%d），按 Origin 字典序接受远端版本（%s > %s）",
				asset.ID, d.localRevision, bundleOrigin, localOrigin)
			return
		}
		d.acceptance = assetSkippedNewer
		d.warning = fmt.Sprintf("资产 %s 本机与远端修订号相同（%d），按 Origin 字典序保留本机版本（%s < %s）",
			asset.ID, d.localRevision, localOrigin, bundleOrigin)
	}
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
