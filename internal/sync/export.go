package sync

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func (s *Service) Export(ctx context.Context, request ExportRequest) (Bundle, error) {
	origin, err := s.Origin(ctx)
	if err != nil {
		return Bundle{}, err
	}
	bundle := Bundle{
		Protocol: ProtocolVersion, Origin: origin, ExportedAt: ids.NowMS(),
		Groups: []GroupPayload{}, Assets: []AssetPayload{}, Credentials: []CredentialPayload{},
	}
	derivedCredentials := map[string]CredentialPayload{}
	includedGroups := map[string]bool{}
	seenAssets := map[string]bool{}

	for _, id := range request.AssetIDs {
		if seenAssets[id] {
			continue
		}
		seenAssets[id] = true
		asset, err := s.store.AssetGet(ctx, id)
		if isNotFound(err) {
			bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("资产 %s 不存在，导出时已跳过", id))
			continue
		}
		if err != nil {
			return Bundle{}, err
		}
		if asset.Builtin || asset.ID == store.BuiltinLocalAssetID {
			continue
		}
		if err := s.collectAncestors(ctx, asset.GroupID, &bundle, includedGroups); err != nil {
			return Bundle{}, err
		}
		payload := payloadFromAsset(asset)
		if request.WithCredentials {
			s.inlineAssetKey(&payload, derivedCredentials, &bundle.Warnings)
		}
		bundle.Assets = append(bundle.Assets, payload)
	}

	if request.WithCredentials {
		seenCredentials := map[string]bool{}
		for _, asset := range bundle.Assets {
			if asset.CredID == nil || *asset.CredID == "" || seenCredentials[*asset.CredID] {
				continue
			}
			credentialID := *asset.CredID
			seenCredentials[credentialID] = true
			if derived, ok := derivedCredentials[credentialID]; ok {
				bundle.Credentials = append(bundle.Credentials, derived)
				continue
			}
			credential, err := s.exportCredential(ctx, credentialID, &bundle.Warnings)
			if isNotFound(err) {
				bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("资产 %s 引用的凭据 %s 不存在", asset.ID, credentialID))
				continue
			}
			if err != nil {
				return Bundle{}, err
			}
			bundle.Credentials = append(bundle.Credentials, credential)
		}
	}
	return bundle, nil
}

func (s *Service) collectAncestors(ctx context.Context, groupID *string, bundle *Bundle, included map[string]bool) error {
	cursor := groupID
	chain := []GroupPayload{}
	visiting := map[string]bool{}
	for depth := 0; cursor != nil && *cursor != ""; depth++ {
		id := *cursor
		if included[id] {
			break
		}
		if visiting[id] {
			bundle.Warnings = append(bundle.Warnings, "分组祖先链存在循环，已停止继续向上收集")
			break
		}
		if depth >= 64 {
			bundle.Warnings = append(bundle.Warnings, "分组祖先链超过 64 层，超出部分未导出")
			break
		}
		visiting[id] = true
		group, err := s.store.GroupGet(ctx, id)
		if isNotFound(err) {
			bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("分组 %s 不存在，祖先链在此处停止", id))
			break
		}
		if err != nil {
			return err
		}
		chain = append(chain, GroupPayload{
			ID: group.ID, ParentID: group.ParentID, Name: group.Name, Sort: group.Sort,
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
		})
		cursor = group.ParentID
	}
	for i := len(chain) - 1; i >= 0; i-- {
		bundle.Groups = append(bundle.Groups, chain[i])
		included[chain[i].ID] = true
	}
	return nil
}

func (s *Service) inlineAssetKey(asset *AssetPayload, derived map[string]CredentialPayload, warnings *[]string) {
	if asset.AuthKind == nil || *asset.AuthKind != "key" || asset.KeyPath == nil || strings.TrimSpace(*asset.KeyPath) == "" {
		return
	}
	path := *asset.KeyPath
	body, err := os.ReadFile(path)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("资产 %s 的私钥文件 %s 无法读取，保留文件引用: %v", asset.ID, path, err))
		return
	}

	id := "synckey-" + asset.ID
	derived[id] = CredentialPayload{
		ID: id, Name: asset.Name + " key", Kind: vault.KindPrivateKey,
		Secret: vault.InlinePrivateKey(string(body), nil).Encode(),
	}
	asset.KeyPath = nil
	asset.CredID = &id
}

func (s *Service) exportCredential(ctx context.Context, id string, warnings *[]string) (CredentialPayload, error) {
	row, err := s.store.CredentialGetRow(ctx, id)
	if err != nil {
		return CredentialPayload{}, err
	}
	if s.vault == nil {
		return CredentialPayload{}, ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	secret, err := s.vault.DecryptCredentialString(ctx, row)
	if err != nil {
		return CredentialPayload{}, err
	}
	if row.Kind == vault.KindPrivateKey {
		payload := vault.ParsePrivateKeyPayload(secret)
		if payload.IsRef() && strings.TrimSpace(*payload.File) != "" {
			path := *payload.File
			body, err := os.ReadFile(path)
			if err != nil {
				*warnings = append(*warnings, fmt.Sprintf("凭据 %s 的私钥文件 %s 无法读取，保留文件引用: %v", id, path, err))
			} else {
				key := string(body)
				payload.Key = &key
				payload.File = nil
				secret = payload.Encode()
			}
		}
	}
	return CredentialPayload{ID: row.ID, Name: row.Name, Kind: row.Kind, Secret: secret}, nil
}
