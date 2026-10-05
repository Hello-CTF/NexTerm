package sync

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func (s *Service) Import(ctx context.Context, request ImportRequest) (ImportReport, error) {
	bundle := request.Bundle
	if bundle.Protocol != ProtocolVersion {
		return ImportReport{}, ipc.NewError(ipc.CodeUnsupported,
			fmt.Sprintf("不支持的同步协议版本 %d（当前支持 %d）", bundle.Protocol, ProtocolVersion))
	}
	if len(bundle.Credentials) > 0 && (s.vault == nil || !s.vault.Status().Unlocked) {
		return ImportReport{}, ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	localOrigin, err := s.Origin(ctx)
	if err != nil {
		return ImportReport{}, err
	}
	report := ImportReport{Warnings: append([]string{}, bundle.Warnings...)}
	assetDecisions := s.planAssets(ctx, bundle.Assets, request.Force, bundle.Origin, localOrigin)
	blocked := blockedCredentials(bundle.Assets, assetDecisions)
	s.importGroups(ctx, bundle.Groups, &report)
	s.importCredentialTombstones(ctx, bundle.CredTombstones, blocked, request.Force, &report)
	s.importCredentials(ctx, bundle.Credentials, blocked, request.Force, &report)
	s.importAssets(ctx, bundle.Assets, assetDecisions, &report)
	s.importSnippets(ctx, bundle.Snippets, request.Force, bundle.Origin, localOrigin, &report)
	return report, nil
}

func (s *Service) importGroups(ctx context.Context, groups []GroupPayload, report *ImportReport) {
	ordered, warnings := orderGroups(groups)
	report.Warnings = append(report.Warnings, warnings...)
	if len(ordered) == 0 {
		return
	}
	existing, err := s.store.GroupList(ctx)
	if err != nil {
		report.Refused += len(ordered)
		report.Warnings = append(report.Warnings, fmt.Sprintf("无法读取本机分组拓扑，已拒绝全部分组导入: %v", err))
		return
	}
	parents := make(map[string]*string, len(existing)+len(ordered))
	for _, group := range existing {
		parents[group.ID] = group.ParentID
	}
	for _, group := range ordered {
		if group.ParentID != nil {
			if strings.TrimSpace(*group.ParentID) == "" {
				group.ParentID = nil
			} else if _, exists := parents[*group.ParentID]; !exists {
				report.Warnings = append(report.Warnings, fmt.Sprintf("分组 %s 的父级 %s 不存在，已按顶级分组导入", group.ID, *group.ParentID))
				group.ParentID = nil
			}
		}
		switch mergedGroupTopology(group.ID, group.ParentID, parents) {
		case groupTopologyCycle:
			report.Warnings = append(report.Warnings, fmt.Sprintf("分组 %s 的父级会在本机与同步拓扑合并后形成循环，已按顶级分组导入", group.ID))
			group.ParentID = nil
		case groupTopologyTooDeep:
			report.Warnings = append(report.Warnings, fmt.Sprintf("分组 %s 合并本机拓扑后的祖先链超过 64 层，已按顶级分组导入", group.ID))
			group.ParentID = nil
		}
		created, err := s.store.GroupUpsert(ctx, group.ID, group.ParentID, group.Name, group.Sort, group.CreatedAt, group.UpdatedAt)
		if err != nil {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("分组 %s 导入失败: %v", group.ID, err))
			continue
		}
		parents[group.ID] = group.ParentID
		if created {
			report.GroupsCreated++
		} else {
			report.GroupsUpdated++
		}
	}
}

func orderGroups(groups []GroupPayload) ([]GroupPayload, []string) {
	byID := make(map[string]GroupPayload, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	state := map[string]uint8{}
	ordered := make([]GroupPayload, 0, len(byID))
	warnings := []string{}
	var visit func(string, int)
	visit = func(id string, depth int) {
		if state[id] == 2 {
			return
		}
		state[id] = 1
		group := byID[id]
		if group.ParentID != nil && *group.ParentID != "" {
			parentID := *group.ParentID
			if _, bundled := byID[parentID]; bundled {
				switch {
				case parentID == id || state[parentID] == 1:
					group.ParentID = nil
					warnings = append(warnings, fmt.Sprintf("分组 %s 的祖先链存在循环，已断开该级引用", id))
				case depth >= 64:
					group.ParentID = nil
					warnings = append(warnings, fmt.Sprintf("分组 %s 的祖先链超过 64 层，已断开该级引用", id))
				default:
					visit(parentID, depth+1)
				}
			}
		}
		state[id] = 2
		byID[id] = group
		ordered = append(ordered, group)
	}
	for _, id := range ids {
		if state[id] == 0 {
			visit(id, 0)
		}
	}
	return ordered, warnings
}

func (s *Service) importCredentials(ctx context.Context, credentials []CredentialPayload, blocked map[string]bool, force bool, report *ImportReport) {
	for _, credential := range credentials {
		if strings.TrimSpace(credential.ID) == "" {
			report.Refused++
			report.Warnings = append(report.Warnings, "拒绝了 ID 为空的凭据")
			continue
		}
		if blocked[credential.ID] {
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 关联的资产因本机版本较新或导入被拒而受到保护，本机凭据保持不变", credential.ID))
			continue
		}
		if !force {
			tombstone, err := s.store.CredentialTombstoneGet(ctx, credential.ID)
			if err != nil && !isNotFound(err) {
				report.Refused++
				report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查凭据 %s 的删除墓碑: %v", credential.ID, err))
				continue
			}
			if err == nil && tombstone.DeletedAt >= credential.UpdatedAt {
				report.SkippedNewer++
				report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
					Kind: "credential", ID: credential.ID, Name: credential.Name,
					LocalRevision: tombstone.DeletedAt, RemoteRevision: credential.UpdatedAt,
					EqualRevision: tombstone.DeletedAt == credential.UpdatedAt,
				})
				report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 已按修订 %d 的删除墓碑移除，忽略修订 %d 的普通凭据项；如需覆盖请使用强制同步",
					credential.ID, tombstone.DeletedAt, credential.UpdatedAt))
				continue
			}
		}
		_, err := s.store.CredentialGetRow(ctx, credential.ID)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查凭据 %s: %v", credential.ID, err))
			continue
		}
		nonce, blob, err := s.vault.EncryptCredential(ctx, credential.Secret)
		if err != nil {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 重加密失败: %v", credential.ID, err))
			continue
		}
		if _, err := s.store.CredentialPut(ctx, store.CredentialInput{
			ID: credential.ID, Name: credential.Name, Kind: credential.Kind,
			Nonce: nonce, Blob: blob, KEKHint: s.vault.KEKHint(),
		}); err != nil {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 导入失败: %v", credential.ID, err))
			continue
		}
		if err := s.store.CredentialTombstoneClear(ctx, credential.ID); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 的删除墓碑清除失败: %v", credential.ID, err))
		}
		if exists {
			report.CredsUpdated++
		} else {
			report.CredsCreated++
		}
		warnMissingReferencedKey(credential, report)
	}
}

func (s *Service) importCredentialTombstones(ctx context.Context, tombstones []store.CredentialTombstone, blocked map[string]bool, force bool, report *ImportReport) {
	for _, tombstone := range tombstones {
		if strings.TrimSpace(tombstone.ID) == "" {
			report.Refused++
			report.Warnings = append(report.Warnings, "拒绝了 ID 为空的凭据墓碑")
			continue
		}
		if blocked[tombstone.ID] {
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 关联的资产因本机版本较新或导入被拒而受到保护，本机凭据保持不变", tombstone.ID))
			continue
		}
		row, err := s.store.CredentialGetRow(ctx, tombstone.ID)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查凭据 %s: %v", tombstone.ID, err))
			continue
		}
		if exists && !force && row.UpdatedAt > tombstone.DeletedAt {
			report.SkippedNewer++
			report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
				Kind: "credential", ID: tombstone.ID, Name: row.Name,
				LocalRevision: row.UpdatedAt, RemoteRevision: tombstone.DeletedAt,
			})
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 的本机版本较新，已忽略远端删除墓碑；如需覆盖请使用强制同步", tombstone.ID))
			continue
		}
		if exists {
			if err := s.store.CredentialDelete(ctx, tombstone.ID); err != nil {
				report.Refused++
				report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 按同步墓碑删除失败: %v", tombstone.ID, err))
				continue
			}
			report.CredsDeleted++
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 已按远端同步墓碑删除（删除时间 %d）", tombstone.ID, tombstone.DeletedAt))
		}
		if err := s.store.CredentialTombstonePut(ctx, tombstone.ID, tombstone.DeletedAt); err != nil {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 的删除墓碑记录失败: %v", tombstone.ID, err))
		}
	}
}

func warnMissingReferencedKey(credential CredentialPayload, report *ImportReport) {
	if credential.Kind != vault.KindPrivateKey {
		return
	}
	payload := vault.ParsePrivateKeyPayload(credential.Secret)
	if !payload.IsRef() || strings.TrimSpace(*payload.File) == "" {
		return
	}
	if _, err := os.Stat(*payload.File); err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("凭据 %s 引用的私钥文件 %s 在目标设备上不可用: %v", credential.ID, *payload.File, err))
	}
}

func (s *Service) importAssets(ctx context.Context, assets []AssetPayload, decisions []assetDecision, report *ImportReport) {
	for i, asset := range assets {
		decision := decisions[i]
		if decision.warning != "" {
			report.Warnings = append(report.Warnings, decision.warning)
		}
		if decision.acceptance != assetAccepted {
			if decision.acceptance == assetSkippedNewer {
				report.SkippedNewer++
				report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
					Kind: "asset", ID: asset.ID, Name: asset.Name,
					LocalRevision: decision.localRevision, RemoteRevision: decision.remoteRevision,
					EqualRevision: decision.equalRevision,
				})
			} else {
				report.Refused++
			}
			continue
		}

		row := asset.storeRow()
		if row.GroupID != nil {
			if strings.TrimSpace(*row.GroupID) == "" {
				row.GroupID = nil
			} else if _, err := s.store.GroupGet(ctx, *row.GroupID); isNotFound(err) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("资产 %s 引用的分组 %s 不存在，已清除该引用", asset.ID, *row.GroupID))
				row.GroupID = nil
			} else if err != nil {
				report.Refused++
				report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查资产 %s 的分组: %v", asset.ID, err))
				continue
			}
		}
		if row.CredID != nil {
			if strings.TrimSpace(*row.CredID) == "" {
				row.CredID = nil
			} else if _, err := s.store.CredentialGetRow(ctx, *row.CredID); isNotFound(err) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("资产 %s 引用的凭据 %s 不存在，已清除该引用", asset.ID, *row.CredID))
				row.CredID = nil
			} else if err != nil {
				report.Refused++
				report.Warnings = append(report.Warnings, fmt.Sprintf("无法检查资产 %s 的凭据: %v", asset.ID, err))
				continue
			}
		}
		if row.DeletedAt == nil && row.KeyPath != nil && strings.TrimSpace(*row.KeyPath) != "" {
			if _, err := os.Stat(*row.KeyPath); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("资产 %s 的私钥文件 %s 在目标设备上不可用: %v", asset.ID, *row.KeyPath, err))
			}
		}
		created, err := s.store.AssetUpsert(ctx, row)
		if err != nil {
			report.Refused++
			report.Warnings = append(report.Warnings, fmt.Sprintf("资产 %s 导入失败: %v", asset.ID, err))
			continue
		}
		if created {
			report.AssetsCreated++
		} else {
			report.AssetsUpdated++
		}
	}
}
