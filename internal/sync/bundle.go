package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// 资产包协议 v1: 面向文件的导入/导出, 与 v2 同步对象协议相互独立。
// 资产包走文件, 不经过同步链路; 摘要/导出/导入在桌面端与服务器端共用同一实现,
// 操作的是当前后端(桌面本机或服务器共享库)的资产表, 身份边界由 /rpc 传输层把关。
const BundleProtocolVersion = 1

type DigestEntry struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Host      *string `json:"host"`
	Username  *string `json:"username"`
	UpdatedAt int64   `json:"updatedAt"`
	DeletedAt *int64  `json:"deletedAt"`
	HasCred   bool    `json:"hasCred"`
	GroupID   *string `json:"groupId"`
}

type SyncDigest struct {
	Origin     string        `json:"origin"`
	Protocol   int           `json:"protocol"`
	AppVersion string        `json:"appVersion"`
	Desktop    bool          `json:"desktop"`
	Assets     []DigestEntry `json:"assets"`
}

type SyncBundleGroup struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Name      string  `json:"name"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

type SyncBundleAsset struct {
	ID          string  `json:"id"`
	GroupID     *string `json:"groupId"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Host        *string `json:"host"`
	Port        *int32  `json:"port"`
	Username    *string `json:"username"`
	AuthKind    *string `json:"authKind"`
	KeyPath     *string `json:"keyPath"`
	CredID      *string `json:"credId"`
	OptionsJSON string  `json:"optionsJson"`
	Tags        string  `json:"tags"`
	Note        string  `json:"note"`
	Sort        int64   `json:"sort"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	DeletedAt   *int64  `json:"deletedAt"`
}

type SyncBundleCredential struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Secret    string `json:"secret"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

type SyncCredentialTombstone struct {
	ID        string `json:"id"`
	DeletedAt int64  `json:"deletedAt"`
}

type SyncBundleSnippet struct {
	ID        string  `json:"id"`
	GroupID   *string `json:"groupId"`
	Name      string  `json:"name"`
	Body      string  `json:"body"`
	Sort      int64   `json:"sort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

type SyncBundle struct {
	Protocol       int                       `json:"protocol"`
	Origin         string                    `json:"origin"`
	ExportedAt     int64                     `json:"exportedAt"`
	Groups         []SyncBundleGroup         `json:"groups"`
	Assets         []SyncBundleAsset         `json:"assets"`
	Creds          []SyncBundleCredential    `json:"creds"`
	CredTombstones []SyncCredentialTombstone `json:"credTombstones,omitempty"`
	Snippets       []SyncBundleSnippet       `json:"snippets,omitempty"`
	Warnings       []string                  `json:"warnings,omitempty"`
}

type SkippedNewerEntry struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	LocalRevision  int64  `json:"localRevision"`
	RemoteRevision int64  `json:"remoteRevision"`
	EqualRevision  bool   `json:"equalRevision"`
}

type ImportReport struct {
	GroupsCreated       int                 `json:"groupsCreated"`
	GroupsUpdated       int                 `json:"groupsUpdated"`
	AssetsCreated       int                 `json:"assetsCreated"`
	AssetsUpdated       int                 `json:"assetsUpdated"`
	CredsCreated        int                 `json:"credsCreated"`
	CredsUpdated        int                 `json:"credsUpdated"`
	CredsDeleted        int                 `json:"credsDeleted"`
	SnippetsCreated     int                 `json:"snippetsCreated"`
	SnippetsUpdated     int                 `json:"snippetsUpdated"`
	SkippedNewer        int                 `json:"skippedNewer"`
	SkippedNewerDetails []SkippedNewerEntry `json:"skippedNewerDetails,omitempty"`
	Refused             int                 `json:"refused"`
	Warnings            []string            `json:"warnings"`
}

type ExportBundleRequest struct {
	AssetIDs  []string `json:"assetIds"`
	WithCreds bool     `json:"withCreds"`
}

type ImportBundleRequest struct {
	Bundle SyncBundle `json:"bundle"`
	Force  bool       `json:"force"`
}

// origin 标识资产包来源端; 服务器共享库与桌面本机各自如实上报。
func (s *Service) origin() string {
	if s.desktop {
		return "desktop"
	}
	return "server"
}

// Digest 列出当前后端的全部非内置资产(含软删), 供导出勾选与导入预览"本机已存在"判定。
func (s *Service) Digest(ctx context.Context) (SyncDigest, error) {
	rows, err := s.store.AssetList(ctx, true)
	if err != nil {
		return SyncDigest{}, err
	}
	entries := make([]DigestEntry, 0, len(rows))
	for _, row := range rows {
		if row.Builtin || row.ID == store.BuiltinLocalAssetID {
			continue
		}
		entries = append(entries, DigestEntry{
			ID: row.ID, Name: row.Name, Kind: row.Kind, Host: row.Host,
			Username: row.Username, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
			HasCred: row.CredID != nil && *row.CredID != "", GroupID: row.GroupID,
		})
	}
	return SyncDigest{
		Origin: s.origin(), Protocol: BundleProtocolVersion, AppVersion: s.appVersion,
		Desktop: s.desktop, Assets: entries,
	}, nil
}

// ExportBundle 把勾选资产打包: 自动带上分组祖先链与全部片段; withCreds 时内联被引用凭据的明文,
// 凭据库必须处于解锁状态, 未勾选凭据时资产只保留凭据引用。
func (s *Service) ExportBundle(ctx context.Context, request ExportBundleRequest) (SyncBundle, error) {
	bundle := SyncBundle{
		Protocol: BundleProtocolVersion, Origin: s.origin(), ExportedAt: ids.NowMS(),
		Groups: []SyncBundleGroup{}, Assets: []SyncBundleAsset{}, Creds: []SyncBundleCredential{},
	}
	rows, err := s.store.AssetList(ctx, true)
	if err != nil {
		return SyncBundle{}, err
	}
	assets := make(map[string]store.AssetRow, len(rows))
	for _, row := range rows {
		assets[row.ID] = row
	}
	groups, err := s.store.GroupList(ctx)
	if err != nil {
		return SyncBundle{}, err
	}
	groupByID := make(map[string]store.AssetGroupRow, len(groups))
	for _, group := range groups {
		groupByID[group.ID] = group
	}
	includedGroups := map[string]bool{}
	var collectGroupAncestors func(groupID *string)
	collectGroupAncestors = func(groupID *string) {
		if groupID == nil || *groupID == "" || includedGroups[*groupID] {
			return
		}
		var chain []store.AssetGroupRow
		visiting := map[string]bool{}
		cursor := *groupID
		for cursor != "" && !includedGroups[cursor] {
			if visiting[cursor] {
				bundle.Warnings = append(bundle.Warnings, "分组祖先链存在循环，已停止继续向上收集")
				break
			}
			visiting[cursor] = true
			group, found := groupByID[cursor]
			if !found {
				bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("分组 %s 不存在，祖先链在此处停止", cursor))
				break
			}
			chain = append(chain, group)
			if group.ParentID == nil {
				cursor = ""
			} else {
				cursor = *group.ParentID
			}
		}
		for index := len(chain) - 1; index >= 0; index-- {
			group := chain[index]
			bundle.Groups = append(bundle.Groups, SyncBundleGroup{
				ID: group.ID, ParentID: group.ParentID, Name: group.Name, Sort: group.Sort,
				CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
			})
			includedGroups[group.ID] = true
		}
	}
	credIDs := []string{}
	credAssetRefs := map[string]string{}
	for _, id := range request.AssetIDs {
		row, found := assets[id]
		if !found || row.Builtin || row.ID == store.BuiltinLocalAssetID {
			continue
		}
		collectGroupAncestors(row.GroupID)
		bundle.Assets = append(bundle.Assets, SyncBundleAsset{
			ID: row.ID, GroupID: row.GroupID, Kind: row.Kind, Name: row.Name,
			Host: row.Host, Port: row.Port, Username: row.Username, AuthKind: row.AuthKind,
			KeyPath: row.KeyPath, CredID: row.CredID, OptionsJSON: row.OptionsJSON,
			Tags: row.Tags, Note: row.Note, Sort: row.Sort, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
		})
		if request.WithCreds && row.CredID != nil && *row.CredID != "" {
			if _, seen := credAssetRefs[*row.CredID]; !seen {
				credAssetRefs[*row.CredID] = row.ID
				credIDs = append(credIDs, *row.CredID)
			}
		}
	}
	if len(credIDs) > 0 {
		if err := s.engine.requireVault(ctx); err != nil {
			return SyncBundle{}, err
		}
		for _, credID := range credIDs {
			credRow, err := s.store.CredentialGetRow(ctx, credID)
			if isNotFound(err) {
				bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("资产 %s 引用的凭据 %s 不存在", credAssetRefs[credID], credID))
				continue
			}
			if err != nil {
				return SyncBundle{}, err
			}
			secret, err := s.vault.DecryptCredentialString(ctx, credRow)
			if err != nil {
				bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("凭据 %s 解密失败, 本次导出跳过该凭据: %v", credID, err))
				continue
			}
			bundle.Creds = append(bundle.Creds, SyncBundleCredential{
				ID: credRow.ID, Name: credRow.Name, Kind: credRow.Kind, Secret: secret, UpdatedAt: credRow.UpdatedAt,
			})
		}
	}
	snippets, err := s.store.SnippetList(ctx)
	if err != nil {
		return SyncBundle{}, err
	}
	for _, snippet := range snippets {
		collectGroupAncestors(snippet.GroupID)
		bundle.Snippets = append(bundle.Snippets, SyncBundleSnippet{
			ID: snippet.ID, GroupID: snippet.GroupID, Name: snippet.Name, Body: snippet.Body,
			Sort: snippet.Sort, CreatedAt: snippet.CreatedAt, UpdatedAt: snippet.UpdatedAt,
		})
	}
	return bundle, nil
}

const (
	bundleAssetAccepted = "accepted"
	bundleAssetSkipped  = "skipped"
	bundleAssetRefused  = "refused"
)

type bundleAssetDecision struct {
	acceptance     string
	warning        string
	localRevision  int64
	remoteRevision int64
	equalRevision  bool
}

// ImportBundle 按资产包协议 v1 写入当前后端: 本机版本较新的条目默认跳过(force 覆盖),
// 非法条目拒绝并记警告; 凭据删除墓碑按修订号裁决, 删除写入同步墓碑防止对端复活。
// 同一对象类的全部写入在单个事务内提交, 任一失败整类回滚, 不留下半导入状态。
func (s *Service) ImportBundle(ctx context.Context, request ImportBundleRequest) (ImportReport, error) {
	bundle := request.Bundle
	if bundle.Protocol != BundleProtocolVersion {
		return ImportReport{}, ipc.NewError(ipc.CodeUnsupported,
			fmt.Sprintf("不支持的同步协议版本 %d（当前支持 %d）", bundle.Protocol, BundleProtocolVersion))
	}
	report := ImportReport{Warnings: append([]string{}, bundle.Warnings...)}
	warnf := func(format string, args ...any) {
		if len(report.Warnings) >= maxSyncWarnings {
			return
		}
		report.Warnings = append(report.Warnings, fmt.Sprintf(format, args...))
	}
	if len(bundle.Creds) > 0 {
		if err := s.engine.requireVault(ctx); err != nil {
			return ImportReport{}, err
		}
	}
	existingAssets := map[string]store.AssetRow{}
	rows, err := s.store.AssetList(ctx, true)
	if err != nil {
		return ImportReport{}, err
	}
	for _, row := range rows {
		existingAssets[row.ID] = row
	}
	decisions := make([]bundleAssetDecision, len(bundle.Assets))
	blockedCredIDs := map[string]bool{}
	for index, payload := range bundle.Assets {
		decision := bundleAssetDecision{acceptance: bundleAssetAccepted}
		existing, found := existingAssets[payload.ID]
		switch {
		case payload.ID == store.BuiltinLocalAssetID || found && existing.Builtin:
			decision.acceptance = bundleAssetRefused
			decision.warning = "内置\"当前设备\"不接受同步覆盖"
		case strings.TrimSpace(payload.ID) == "" || !ids.Valid(payload.ID) || strings.TrimSpace(payload.Name) == "":
			decision.acceptance = bundleAssetRefused
			id := strings.TrimSpace(payload.ID)
			if id == "" {
				id = "(空)"
			}
			decision.warning = fmt.Sprintf("资产 %s 的 ID 或名称不合法，已拒绝导入", id)
		case found:
			decision.localRevision = objectRevision(existing.UpdatedAt, existing.DeletedAt)
			decision.remoteRevision = objectRevision(payload.UpdatedAt, payload.DeletedAt)
			decision.equalRevision = decision.localRevision == decision.remoteRevision
			if !request.Force && decision.localRevision > decision.remoteRevision {
				decision.acceptance = bundleAssetSkipped
				decision.warning = fmt.Sprintf("资产 %s 的本机版本较新，已跳过；如需覆盖请使用强制同步", payload.ID)
			}
		default:
			decision.remoteRevision = objectRevision(payload.UpdatedAt, payload.DeletedAt)
		}
		decisions[index] = decision
		if payload.CredID != nil && *payload.CredID != "" && decision.acceptance != bundleAssetAccepted {
			blockedCredIDs[*payload.CredID] = true
		}
	}
	groupPlans, err := s.planGroupImports(ctx, bundle.Groups, request.Force, &report, warnf)
	if err != nil {
		return ImportReport{}, err
	}
	// 两遍写入: 先全部按顶级分组落库(任意顺序都无 FK 依赖), 再链接父级; 整类一个事务。
	if err := s.inImportTx(ctx, func(tx *sql.Tx) error {
		for _, plan := range groupPlans {
			if err := s.engine.groupUpsertTx(ctx, tx, groupObject{
				ID: plan.id, Name: plan.name, Sort: plan.sort, CreatedAt: plan.createdAt, UpdatedAt: plan.updatedAt,
			}, nil); err != nil {
				return err
			}
		}
		for _, plan := range groupPlans {
			if plan.parentID == nil {
				continue
			}
			if err := s.engine.groupUpsertTx(ctx, tx, groupObject{
				ID: plan.id, ParentID: plan.parentID, Name: plan.name, Sort: plan.sort,
				CreatedAt: plan.createdAt, UpdatedAt: plan.updatedAt,
			}, plan.parentID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	for _, plan := range groupPlans {
		if plan.exists {
			report.GroupsUpdated++
		} else {
			report.GroupsCreated++
		}
	}
	type preparedCred struct {
		id        string
		name      string
		kind      string
		updatedAt int64
		nonce     []byte
		blob      []byte
		exists    bool
	}
	preparedCreds := make([]preparedCred, 0, len(bundle.Creds))
	seenCredIDs := map[string]bool{}
	for _, payload := range bundle.Creds {
		id := strings.TrimSpace(payload.ID)
		if id == "" {
			report.Refused++
			warnf("拒绝了 ID 为空的凭据")
			continue
		}
		if !ids.Valid(id) {
			report.Refused++
			warnf("凭据 %s 的 ID 格式不合法，已拒绝导入", id)
			continue
		}
		if blockedCredIDs[id] {
			warnf("凭据 %s 关联的资产因本机版本较新或导入被拒而受到保护，本机凭据保持不变", id)
			continue
		}
		name := strings.TrimSpace(payload.Name)
		if name == "" {
			name = "凭据"
		}
		kind := strings.TrimSpace(payload.Kind)
		if kind == "" {
			kind = "password"
		}
		updatedAt := payload.UpdatedAt
		if updatedAt <= 0 {
			updatedAt = ids.NowMS()
		}
		if tombstone, err := s.store.CredentialTombstoneGet(ctx, id); err == nil && !request.Force && tombstone.DeletedAt >= updatedAt {
			report.SkippedNewer++
			report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
				Kind: "credential", ID: id, Name: name,
				LocalRevision: tombstone.DeletedAt, RemoteRevision: updatedAt,
				EqualRevision: tombstone.DeletedAt == updatedAt,
			})
			warnf("凭据 %s 的本机删除标记较新，已跳过导入；如需恢复请使用强制同步", id)
			continue
		} else if err != nil && !isNotFound(err) {
			return ImportReport{}, err
		}
		_, err := s.store.CredentialGetRow(ctx, id)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return ImportReport{}, err
		}
		nonce, blob, err := s.vault.EncryptCredential(ctx, payload.Secret)
		if err != nil {
			return ImportReport{}, err
		}
		preparedCreds = append(preparedCreds, preparedCred{
			id: id, name: name, kind: kind, updatedAt: updatedAt,
			nonce: nonce, blob: blob, exists: exists || seenCredIDs[id],
		})
		seenCredIDs[id] = true
	}
	if err := s.inImportTx(ctx, func(tx *sql.Tx) error {
		for _, cred := range preparedCreds {
			if err := s.engine.credentialUpsertTx(ctx, tx, credentialObject{
				ID: cred.id, Name: cred.name, Kind: cred.kind, UpdatedAt: cred.updatedAt,
			}, cred.nonce, cred.blob); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	for _, cred := range preparedCreds {
		if cred.exists {
			report.CredsUpdated++
		} else {
			report.CredsCreated++
		}
	}
	preparedAssets := make([]store.AssetRow, 0, len(bundle.Assets))
	for index, payload := range bundle.Assets {
		decision := decisions[index]
		if decision.acceptance != bundleAssetAccepted {
			if decision.acceptance == bundleAssetSkipped {
				report.SkippedNewer++
				report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
					Kind: "asset", ID: payload.ID, Name: payload.Name,
					LocalRevision: decision.localRevision, RemoteRevision: decision.remoteRevision,
					EqualRevision: decision.equalRevision,
				})
			} else {
				report.Refused++
			}
			warnf("%s", decision.warning)
			continue
		}
		optionsJSON := payload.OptionsJSON
		if !validJSONObject(optionsJSON) {
			if strings.TrimSpace(optionsJSON) != "" {
				warnf("资产 %s 的 optionsJson 不是合法 JSON，已按空处理", payload.ID)
			}
			optionsJSON = "{}"
		}
		row := store.AssetRow{
			ID: payload.ID, GroupID: payload.GroupID, Kind: payload.Kind, Name: payload.Name,
			Host: payload.Host, Port: payload.Port, Username: payload.Username, AuthKind: payload.AuthKind,
			KeyPath: payload.KeyPath, CredID: payload.CredID, OptionsJSON: optionsJSON,
			Tags: payload.Tags, Note: payload.Note, Sort: payload.Sort,
			CreatedAt: payload.CreatedAt, DeletedAt: payload.DeletedAt,
		}
		if row.Kind == "" {
			row.Kind = "ssh"
		}
		if row.AuthKind == nil || *row.AuthKind == "" {
			password := "password"
			row.AuthKind = &password
		}
		if row.KeyPath != nil && strings.TrimSpace(*row.KeyPath) == "" {
			row.KeyPath = nil
		}
		if row.CreatedAt <= 0 {
			row.CreatedAt = ids.NowMS()
		}
		row.UpdatedAt = payload.UpdatedAt
		if row.UpdatedAt <= 0 {
			row.UpdatedAt = ids.NowMS()
		}
		if row.CredID != nil {
			if *row.CredID == "" {
				row.CredID = nil
			} else if _, err := s.store.CredentialGetRow(ctx, *row.CredID); isNotFound(err) {
				warnf("资产 %s 引用的凭据 %s 不存在，已清除该引用", payload.ID, *row.CredID)
				row.CredID = nil
			} else if err != nil {
				return ImportReport{}, err
			}
		}
		if row.GroupID != nil {
			if *row.GroupID == "" {
				row.GroupID = nil
			} else if _, err := s.store.GroupGet(ctx, *row.GroupID); isNotFound(err) {
				warnf("资产 %s 引用的分组 %s 不存在，已清除该引用", payload.ID, *row.GroupID)
				row.GroupID = nil
			} else if err != nil {
				return ImportReport{}, err
			}
		}
		preparedAssets = append(preparedAssets, row)
	}
	createdFlags := make([]bool, len(preparedAssets))
	if err := s.inImportTx(ctx, func(tx *sql.Tx) error {
		for index, row := range preparedAssets {
			created, err := s.store.AssetUpsertTx(ctx, tx, row)
			if err != nil {
				return err
			}
			createdFlags[index] = created
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	for _, created := range createdFlags {
		if created {
			report.AssetsCreated++
		} else {
			report.AssetsUpdated++
		}
	}
	type preparedCredTombstone struct {
		id        string
		deletedAt int64
	}
	preparedTombstones := make([]preparedCredTombstone, 0, len(bundle.CredTombstones))
	for _, tombstone := range bundle.CredTombstones {
		id := strings.TrimSpace(tombstone.ID)
		if id == "" {
			report.Refused++
			warnf("拒绝了 ID 为空的凭据删除墓碑")
			continue
		}
		if !ids.Valid(id) {
			report.Refused++
			warnf("凭据删除墓碑 %s 的 ID 格式不合法，已拒绝导入", id)
			continue
		}
		local, err := s.store.CredentialGetRow(ctx, id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return ImportReport{}, err
		}
		if !request.Force && local.UpdatedAt > tombstone.DeletedAt {
			report.SkippedNewer++
			report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
				Kind: "credential", ID: id, Name: local.Name,
				LocalRevision: local.UpdatedAt, RemoteRevision: tombstone.DeletedAt,
			})
			warnf("凭据 %s 的本机版本较新，已忽略远端删除墓碑；如需覆盖请使用强制同步", id)
			continue
		}
		preparedTombstones = append(preparedTombstones, preparedCredTombstone{id: id, deletedAt: tombstone.DeletedAt})
	}
	if err := s.inImportTx(ctx, func(tx *sql.Tx) error {
		for _, tombstone := range preparedTombstones {
			if _, err := tx.ExecContext(ctx, "DELETE FROM credential WHERE id = ?", tombstone.id); err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO credential_tombstone(id, deleted_at) VALUES(?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at=`+scalarMax(s.store.Backend())+`(credential_tombstone.deleted_at, excluded.deleted_at)`,
				tombstone.id, tombstone.deletedAt); err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	report.CredsDeleted += len(preparedTombstones)
	type preparedSnippet struct {
		row    store.SnippetRow
		exists bool
	}
	preparedSnippets := make([]preparedSnippet, 0, len(bundle.Snippets))
	seenSnippetIDs := map[string]bool{}
	for _, payload := range bundle.Snippets {
		id := strings.TrimSpace(payload.ID)
		if id == "" || !ids.Valid(id) || strings.TrimSpace(payload.Name) == "" {
			report.Refused++
			name := id
			if name == "" {
				name = "(空)"
			}
			warnf("片段 %s 的 ID 或名称不合法，已拒绝导入", name)
			continue
		}
		updatedAt := payload.UpdatedAt
		if updatedAt <= 0 {
			updatedAt = ids.NowMS()
		}
		if tombstone, found, err := s.engine.syncTombstoneGet(ctx, id); err != nil {
			return ImportReport{}, err
		} else if found && !request.Force && tombstone.DeletedAt >= updatedAt {
			report.SkippedNewer++
			report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
				Kind: "snippet", ID: id, Name: strings.TrimSpace(payload.Name),
				LocalRevision: tombstone.DeletedAt, RemoteRevision: updatedAt,
				EqualRevision: tombstone.DeletedAt == updatedAt,
			})
			warnf("片段 %s 的本机删除标记较新，已跳过导入；如需恢复请使用强制同步", id)
			continue
		}
		existing, err := s.store.SnippetGet(ctx, id)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return ImportReport{}, err
		}
		if exists && !request.Force && existing.UpdatedAt > updatedAt {
			report.SkippedNewer++
			report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
				Kind: "snippet", ID: id, Name: existing.Name,
				LocalRevision: existing.UpdatedAt, RemoteRevision: updatedAt,
			})
			warnf("片段 %s 的本机版本较新，已跳过；如需覆盖请使用强制同步", id)
			continue
		}
		name := strings.TrimSpace(payload.Name)
		body := payload.Body
		sort := payload.Sort
		createdAt := payload.CreatedAt
		if createdAt <= 0 {
			createdAt = ids.NowMS()
		}
		groupID := payload.GroupID
		if groupID != nil && *groupID == "" {
			groupID = nil
		}
		if groupID != nil {
			if _, err := s.store.GroupGet(ctx, *groupID); isNotFound(err) {
				warnf("片段 %s 引用的分组 %s 不存在，已清除该引用", id, *groupID)
				groupID = nil
			} else if err != nil {
				return ImportReport{}, err
			}
		}
		preparedSnippets = append(preparedSnippets, preparedSnippet{
			row: store.SnippetRow{
				ID: id, GroupID: groupID, Name: name, Body: body,
				Sort: sort, CreatedAt: createdAt, UpdatedAt: updatedAt,
			},
			exists: exists || seenSnippetIDs[id],
		})
		seenSnippetIDs[id] = true
	}
	if err := s.inImportTx(ctx, func(tx *sql.Tx) error {
		for _, snippet := range preparedSnippets {
			if err := s.engine.snippetUpsertTx(ctx, tx, snippet.row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	for _, snippet := range preparedSnippets {
		if snippet.exists {
			report.SnippetsUpdated++
		} else {
			report.SnippetsCreated++
		}
	}
	return report, nil
}

// inImportTx 在单个事务内提交一个对象类的全部写入, 任一语句失败整类回滚。
func (s *Service) inImportTx(ctx context.Context, apply func(*sql.Tx) error) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := apply(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func validJSONObject(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	var decoded map[string]json.RawMessage
	return json.Unmarshal([]byte(trimmed), &decoded) == nil && decoded != nil
}

type bundleGroupPlan struct {
	id        string
	name      string
	parentID  *string
	sort      int64
	createdAt int64
	updatedAt int64
	exists    bool
}

// planGroupImports 校验分组载荷并解析父级: 同一 trim 后非空 ID 只处理包内最后一项(last-index 去重),
// 墓碑判断/skipped 计数/plan 构建/父级引用统一采用最终决策; 本机删除墓碑较新且非 force 时跳过(保留墓碑);
// 父级只允许指向包内或通过本地存在性校验的分组, 悬空父级置 nil; 循环与超深经 mergedGroupTopology 兜底。
func (s *Service) planGroupImports(ctx context.Context, payloads []SyncBundleGroup, force bool, report *ImportReport, warnf func(string, ...any)) ([]bundleGroupPlan, error) {
	lastIndex := make(map[string]int, len(payloads))
	for index, payload := range payloads {
		if id := strings.TrimSpace(payload.ID); id != "" {
			lastIndex[id] = index
		}
	}
	plans := make([]bundleGroupPlan, 0, len(payloads))
	planIndex := map[string]int{}
	for index, payload := range payloads {
		id := strings.TrimSpace(payload.ID)
		if id != "" && lastIndex[id] != index {
			continue
		}
		if id == "" {
			report.Refused++
			warnf("拒绝了 ID 为空的分组")
			continue
		}
		if !ids.Valid(id) {
			report.Refused++
			warnf("分组 %s 的 ID 格式不合法，已拒绝导入", id)
			continue
		}
		name := strings.TrimSpace(payload.Name)
		if name == "" {
			name = "分组"
		}
		createdAt, updatedAt := payload.CreatedAt, payload.UpdatedAt
		if createdAt <= 0 {
			createdAt = ids.NowMS()
		}
		if updatedAt <= 0 {
			updatedAt = ids.NowMS()
		}
		if tombstone, found, err := s.engine.syncTombstoneGet(ctx, id); err != nil {
			return nil, err
		} else if found && !force && tombstone.DeletedAt >= updatedAt {
			report.SkippedNewer++
			report.SkippedNewerDetails = append(report.SkippedNewerDetails, SkippedNewerEntry{
				Kind: "group", ID: id, Name: name,
				LocalRevision: tombstone.DeletedAt, RemoteRevision: updatedAt,
				EqualRevision: tombstone.DeletedAt == updatedAt,
			})
			warnf("分组 %s 的本机删除标记较新，已跳过导入；如需恢复请使用强制同步", id)
			continue
		}
		parentID := payload.ParentID
		if parentID != nil && *parentID == "" {
			parentID = nil
		}
		_, err := s.store.GroupGet(ctx, id)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		planIndex[id] = len(plans)
		plans = append(plans, bundleGroupPlan{
			id: id, name: name, parentID: parentID, sort: payload.Sort,
			createdAt: createdAt, updatedAt: updatedAt, exists: exists,
		})
	}
	parents, err := s.engine.groupParents(ctx)
	if err != nil {
		return nil, err
	}
	for index := range plans {
		plan := &plans[index]
		if plan.parentID == nil {
			continue
		}
		if _, inBundle := planIndex[*plan.parentID]; inBundle {
			continue
		}
		if _, err := s.store.GroupGet(ctx, *plan.parentID); isNotFound(err) {
			warnf("分组 %s 的父级 %s 不存在, 已按顶级分组导入", plan.id, *plan.parentID)
			plan.parentID = nil
		} else if err != nil {
			return nil, err
		}
	}
	for index := range plans {
		parents[plans[index].id] = plans[index].parentID
	}
	for index := range plans {
		plan := &plans[index]
		if plan.parentID == nil {
			continue
		}
		switch mergedGroupTopology(plan.id, plan.parentID, parents) {
		case groupTopologyCycle:
			warnf("分组 %s 的父级会在合并后形成循环, 已按顶级分组导入", plan.id)
			plan.parentID = nil
			parents[plan.id] = nil
		case groupTopologyTooDeep:
			warnf("分组 %s 合并后的祖先链超过 64 层, 已按顶级分组导入", plan.id)
			plan.parentID = nil
			parents[plan.id] = nil
		}
	}
	return plans, nil
}
