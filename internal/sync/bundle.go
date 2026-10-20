package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Asset bundle protocol v1: file-oriented import/export, independent of the v2 sync object protocol.
// Bundles travel as files, not through the sync link; digest/export/import share one implementation
// across desktop and server, operating on the asset tables of the current backend (desktop local or server shared library); the /rpc transport layer enforces the identity boundary.
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
	CredsOrphaned       int                 `json:"credsOrphaned"`
	SnippetsCreated     int                 `json:"snippetsCreated"`
	SnippetsUpdated     int                 `json:"snippetsUpdated"`
	SkippedNewer        int                 `json:"skippedNewer"`
	SkippedNewerDetails []SkippedNewerEntry `json:"skippedNewerDetails,omitempty"`
	Conflicts           int                 `json:"conflicts"`
	ConflictDetails     []SkippedNewerEntry `json:"conflictDetails,omitempty"`
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

// origin identifies which side produced the bundle; the server shared library and the desktop local backend each report truthfully.
func (s *Service) origin() string {
	if s.desktop {
		return "desktop"
	}
	return "server"
}

// Digest lists all non-built-in assets of the current backend (including soft-deleted), for export selection and import preview "already exists locally" checks.
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

// ExportBundle packs the selected assets: it automatically includes group ancestor chains and all snippets; with withCreds the plaintext of referenced credentials is inlined,
// which requires the vault to be unlocked; unselected credentials leave assets with credential references only.
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
				bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("凭据 %s 解密失败，导出时已跳过：%v", credID, err))
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

// preparedCred is one credential payload validated and encrypted for an import.
type preparedCred struct {
	id        string
	name      string
	kind      string
	updatedAt int64
	nonce     []byte
	blob      []byte
	exists    bool
}

// ImportBundle writes the bundle into the current backend per asset bundle protocol v1: entries whose local version is newer are skipped by default (force overrides),
// invalid entries are rejected with a warning; credential deletion tombstones are decided by revision, and deletions write sync tombstones to prevent revival on the peer.
// Groups keep their own committed transaction; credentials and assets share one transaction, so an asset-save failure rolls back the credential
// creates/updates instead of leaving them orphaned. Asset writes bind to the pre-import snapshot with an expected-revision guard: an asset modified
// between the decision and the write is reported as a conflict and skipped (per-statement savepoints keep the rest of the import intact), never blindly
// overwritten. After a successful replacement, credentials that the import unbound and that no asset references anymore are cleaned up.
func (s *Service) ImportBundle(ctx context.Context, request ImportBundleRequest) (ImportReport, error) {
	// Serialize against sync rounds: pull-merge and the import must not interleave
	// their snapshot decisions and writes on the same replica.
	s.engine.opMu.Lock()
	defer s.engine.opMu.Unlock()
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
	// Last-index dedup mirrors planGroupImports: for duplicate in-bundle IDs only the
	// final entry is decided and written; dropped duplicates stay visible as warnings.
	lastAssetIndex := make(map[string]int, len(bundle.Assets))
	for index, payload := range bundle.Assets {
		if id := strings.TrimSpace(payload.ID); id != "" {
			lastAssetIndex[id] = index
		}
	}
	groupPlans, err := s.planGroupImports(ctx, bundle.Groups, request.Force, &report, warnf)
	if err != nil {
		return ImportReport{}, err
	}
	groupPlanIDs := make(map[string]bool, len(groupPlans))
	for _, plan := range groupPlans {
		groupPlanIDs[plan.id] = true
	}
	// Asset decisions run before credential preparation: a credential whose only
	// referencing asset is skipped or refused must stay untouched locally.
	decisions := make(map[int]bundleAssetDecision, len(bundle.Assets))
	blockedCredIDs := map[string]bool{}
	for index, payload := range bundle.Assets {
		if id := strings.TrimSpace(payload.ID); id != "" && lastAssetIndex[id] != index {
			continue
		}
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
				id = "（空）"
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
	// Two-pass group write: first store everything as top-level groups (any order, no FK dependencies), then link parents.
	// Groups keep their own committed transaction: a later class failure never rolls them back.
	if err := s.inImportTx(ctx, func(tx *gorm.DB) error {
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
	preparedCreds := make([]preparedCred, 0, len(bundle.Creds))
	// priorCredRows/priorCredTombstones snapshot the pre-import state of every upserted
	// credential: an asset that conflicts at write time must not leave the credential
	// created, overwritten, or its tombstone cleared, so the import compensates back to
	// exactly this state before commit.
	priorCredRows := map[string]*store.CredentialRow{}
	priorCredTombstones := map[string]*store.CredentialTombstone{}
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
			warnf("凭据 %s 关联的资产版本较新或导入被拒，本机凭据保持不变", id)
			continue
		}
		if seenCredIDs[id] {
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
		} else if err == nil {
			priorCredTombstones[id] = &tombstone
		}
		existing, err := s.store.CredentialGetRow(ctx, id)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return ImportReport{}, err
		}
		if exists {
			row := existing
			priorCredRows[id] = &row
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
	type preparedAsset struct {
		row      store.AssetRow
		base     *store.AssetRow
		accepted bool
		skipped  bool
		refused  bool
	}
	preparedAssets := make([]preparedAsset, 0, len(bundle.Assets))
	for index, payload := range bundle.Assets {
		if id := strings.TrimSpace(payload.ID); id != "" && lastAssetIndex[id] != index {
			warnf("资产 %s 在包内重复，仅导入最后一个", id)
			continue
		}
		decision := decisions[index]
		existing, found := existingAssets[payload.ID]
		entry := preparedAsset{accepted: decision.acceptance == bundleAssetAccepted, skipped: decision.acceptance == bundleAssetSkipped, refused: decision.acceptance == bundleAssetRefused}
		if found {
			row := existing
			entry.base = &row
		}
		if !entry.accepted {
			if entry.skipped {
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
			preparedAssets = append(preparedAssets, entry)
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
			} else if seenCredIDs[*row.CredID] {
				// The credential is written by this import in the same transaction.
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
			} else if groupPlanIDs[*row.GroupID] {
				// The group is written by this import's group transaction.
			} else if _, err := s.store.GroupGet(ctx, *row.GroupID); isNotFound(err) {
				warnf("资产 %s 引用的分组 %s 不存在，已清除该引用", payload.ID, *row.GroupID)
				row.GroupID = nil
			} else if err != nil {
				return ImportReport{}, err
			}
		}
		entry.row = row
		preparedAssets = append(preparedAssets, entry)
	}
	// Credential tombstones must not delete credentials the imported assets bind:
	// the delete would silently clear the fresh binding via OnDelete:SET NULL.
	boundCredIDs := map[string]bool{}
	for _, entry := range preparedAssets {
		if entry.accepted && entry.row.CredID != nil && *entry.row.CredID != "" {
			boundCredIDs[*entry.row.CredID] = true
		}
	}
	type preparedCredTombstone struct {
		id        string
		deletedAt int64
	}
	preparedTombstones := make([]preparedCredTombstone, 0, len(bundle.CredTombstones))
	tombstonedCredIDs := map[string]bool{}
	for _, tombstone := range bundle.CredTombstones {
		id := strings.TrimSpace(tombstone.ID)
		if id == "" {
			report.Refused++
			warnf("拒绝了 ID 为空的凭据删除标记")
			continue
		}
		if !ids.Valid(id) {
			report.Refused++
			warnf("凭据删除标记 %s 的 ID 格式不合法，已拒绝导入", id)
			continue
		}
		if boundCredIDs[id] {
			warnf("凭据 %s 仍被本次导入的资产引用，已忽略其删除标记", id)
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
			warnf("凭据 %s 的本机版本较新，已忽略远端删除标记；如需覆盖请使用强制同步", id)
			continue
		}
		preparedTombstones = append(preparedTombstones, preparedCredTombstone{id: id, deletedAt: tombstone.DeletedAt})
		tombstonedCredIDs[id] = true
	}
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
				name = "（空）"
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
			if groupPlanIDs[*groupID] {
				// The group is written by this import's group transaction.
			} else if _, err := s.store.GroupGet(ctx, *groupID); isNotFound(err) {
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
	createdFlags := make([]bool, len(preparedAssets))
	conflicted := make([]bool, len(preparedAssets))
	compensatedCredIDs := map[string]bool{}
	// Credentials and assets share one transaction: an asset-save failure rolls the
	// credential creates/updates back instead of leaving them orphaned.
	if err := s.inImportTx(ctx, func(tx *gorm.DB) error {
		for _, cred := range preparedCreds {
			// The write binds to the snapshot's revision AND content for an existing
			// credential (a later local write is never overwritten) and to must-not-exist
			// for a new one. A conflict is recorded per-credential and skipped, never
			// overwriting the later local write.
			if err := s.importCredentialTx(ctx, tx, cred, priorCredRows[cred.id], &report, warnf); err != nil {
				return err
			}
		}
		for index, entry := range preparedAssets {
			if !entry.accepted {
				continue
			}
			created, err := s.importAssetTx(ctx, tx, index, entry.row, entry.base)
			if err != nil {
				if errors.Is(err, store.ErrWriteConflict) {
					conflicted[index] = true
					report.Conflicts++
					localRevision := int64(0)
					if entry.base != nil {
						localRevision = objectRevision(entry.base.UpdatedAt, entry.base.DeletedAt)
					}
					report.ConflictDetails = append(report.ConflictDetails, SkippedNewerEntry{
						Kind: "asset", ID: entry.row.ID, Name: entry.row.Name,
						LocalRevision:  localRevision,
						RemoteRevision: objectRevision(entry.row.UpdatedAt, entry.row.DeletedAt),
					})
					warnf("资产 %s 在导入决策后已被本机更新，已跳过（可重新导入覆盖）", entry.row.ID)
					continue
				}
				return err
			}
			createdFlags[index] = created
		}
		// An asset that conflicted at write time must not take its credentials down with
		// it: a credential referenced only by conflicted assets is compensated back to its
		// pre-import state (created ones removed, overwritten ones restored, cleared
		// tombstones re-recorded) before the transaction commits. Credentials shared with
		// successfully written assets stay.
		writtenCredRefs := map[string]bool{}
		for index, entry := range preparedAssets {
			if entry.accepted && !conflicted[index] && entry.row.CredID != nil && *entry.row.CredID != "" {
				writtenCredRefs[*entry.row.CredID] = true
			}
		}
		for index, entry := range preparedAssets {
			if !conflicted[index] || entry.row.CredID == nil || *entry.row.CredID == "" {
				continue
			}
			credID := *entry.row.CredID
			// Only a credential this import actually upserted can be compensated: one that
			// is absent from bundle.Creds was never written here and must survive untouched.
			if !seenCredIDs[credID] || writtenCredRefs[credID] || compensatedCredIDs[credID] {
				continue
			}
			compensatedCredIDs[credID] = true
			if err := s.compensateCredentialTx(ctx, tx, credID, priorCredRows[credID], priorCredTombstones[credID]); err != nil {
				return err
			}
			warnf("凭据 %s 引用的资产在写入时冲突，已回滚该凭据的导入写入", credID)
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	for _, cred := range preparedCreds {
		if compensatedCredIDs[cred.id] {
			continue
		}
		if cred.exists {
			report.CredsUpdated++
		} else {
			report.CredsCreated++
		}
	}
	for index, entry := range preparedAssets {
		if !entry.accepted || conflicted[index] {
			continue
		}
		if createdFlags[index] {
			report.AssetsCreated++
		} else {
			report.AssetsUpdated++
		}
	}
	if err := s.inImportTx(ctx, func(tx *gorm.DB) error {
		credential := query.Use(tx).CredentialRow
		for _, tombstone := range preparedTombstones {
			if _, err := credential.WithContext(ctx).
				Where(credential.ID.Eq(tombstone.id)).
				Where(ownerConditions(ctx, credential.TableName())...).
				Delete(); err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"deleted_at": store.MaxExpression(tx, "credential_tombstone", "deleted_at", nil),
				}),
				Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
			}).Create(&store.CredentialTombstone{ID: tombstone.id, DeletedAt: tombstone.deletedAt, OwnerID: ipc.OwnerID(ctx)}).Error; err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
		}
		return nil
	}); err != nil {
		return ImportReport{}, err
	}
	report.CredsDeleted += len(preparedTombstones)
	if err := s.inImportTx(ctx, func(tx *gorm.DB) error {
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
	// Orphan cleanup after successful replacement: a credential the import unbound and that
	// no asset references anymore is removed (soft-deleted assets keep their binding for a
	// potential restore and count as references). Credentials written or tombstoned by this
	// bundle are left alone.
	orphanCandidates := map[string]bool{}
	for index, entry := range preparedAssets {
		if !entry.accepted || conflicted[index] || entry.base == nil || entry.base.CredID == nil || *entry.base.CredID == "" {
			continue
		}
		old := *entry.base.CredID
		replacement := ""
		if entry.row.CredID != nil {
			replacement = *entry.row.CredID
		}
		if old == replacement || seenCredIDs[old] || tombstonedCredIDs[old] {
			continue
		}
		orphanCandidates[old] = true
	}
	for id := range orphanCandidates {
		deleted, err := s.store.CredentialDeleteIfUnreferenced(ctx, id)
		if err != nil {
			warnf("清理旧凭据 %s 失败：%v", id, err)
			continue
		}
		if deleted {
			report.CredsOrphaned++
			warnf("凭据 %s 已不再被任何资产引用，已清理", id)
		}
	}
	return report, nil
}

// importAssetTx writes one prepared asset inside the import transaction, demarcated by a
// savepoint so a conflict or failure rolls back only this asset's statements.
func (s *Service) importAssetTx(ctx context.Context, tx *gorm.DB, index int, row store.AssetRow, base *store.AssetRow) (bool, error) {
	savepoint := fmt.Sprintf("import_asset_%d", index)
	if err := tx.SavePoint(savepoint).Error; err != nil {
		return false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	created, err := s.store.AssetUpsertGuardedTx(ctx, tx, row, base)
	if err == nil {
		return created, nil
	}
	if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
		return false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+rollbackErr.Error(), rollbackErr)
	}
	return false, err
}

// importCredentialTx writes one prepared credential inside the import transaction with a
// savepoint: a conflict (the credential changed after the snapshot) is recorded and the
// credential is skipped, never overwriting the later local write.
func (s *Service) importCredentialTx(ctx context.Context, tx *gorm.DB, cred preparedCred, snapshot *store.CredentialRow, report *ImportReport, warnf func(string, ...any)) error {
	savepoint := fmt.Sprintf("import_credential_%s", cred.id)
	if err := tx.SavePoint(savepoint).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	err := s.engine.credentialUpsertTxBound(ctx, tx, credentialObject{
		ID: cred.id, Name: cred.name, Kind: cred.kind, UpdatedAt: cred.updatedAt,
	}, cred.nonce, cred.blob, snapshot)
	if err == nil {
		return nil
	}
	if rollbackErr := tx.RollbackTo(savepoint).Error; rollbackErr != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+rollbackErr.Error(), rollbackErr)
	}
	if errors.Is(err, store.ErrWriteConflict) {
		report.Conflicts++
		report.ConflictDetails = append(report.ConflictDetails, SkippedNewerEntry{
			Kind: "credential", ID: cred.id, Name: cred.name,
		})
		warnf("凭据 %s 在导入决策后已被本机更新，已跳过（可重新导入覆盖）", cred.id)
		return nil
	}
	return err
}

// compensateCredentialTx rolls one credential back to its pre-import state after the only
// asset referencing it conflicted at write time: a credential created by this import is
// removed, an overwritten one is restored field-by-field (including the original revision),
// and a tombstone cleared by the upsert is re-recorded. A nil prior row means the
// credential did not exist before the import.
func (s *Service) compensateCredentialTx(ctx context.Context, tx *gorm.DB, credID string, prior *store.CredentialRow, priorTombstone *store.CredentialTombstone) error {
	credential := query.Use(tx).CredentialRow
	if prior == nil {
		if _, err := credential.WithContext(ctx).
			Where(credential.ID.Eq(credID)).
			Where(ownerConditions(ctx, credential.TableName())...).
			Delete(); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	} else {
		if _, err := credential.WithContext(ctx).
			Where(credential.ID.Eq(credID)).
			Where(ownerConditions(ctx, credential.TableName())...).
			UpdateColumns(map[string]any{
				"name": prior.Name, "kind": prior.Kind, "cipher": prior.Cipher,
				"nonce": prior.Nonce, "blob": prior.Blob, "kek_hint": prior.KEKHint,
				"created_at": prior.CreatedAt, "updated_at": prior.UpdatedAt,
			}); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	}
	if priorTombstone != nil {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "owner_id"}, {Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"deleted_at"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
		}).Create(priorTombstone).Error; err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	}
	return nil
}

// inImportTx commits all writes of one object class in a single transaction; any statement failure rolls back the whole class.
func (s *Service) inImportTx(ctx context.Context, apply func(*gorm.DB) error) error {
	var applyErr error
	err := s.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		applyErr = apply(tx)
		return applyErr
	})
	if applyErr != nil {
		return applyErr
	}
	if err != nil {
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

// planGroupImports validates group payloads and resolves parents: each non-empty ID after trim only processes the last in-bundle entry (last-index dedup);
// tombstone checks, skipped counts, plan building, and parent references all use the final decision; a newer local deletion tombstone skips the entry unless force (tombstone kept);
// parents may only point to in-bundle groups or groups passing local existence checks, dangling parents become nil; cycles and excessive depth fall back to mergedGroupTopology.
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
			warnf("分组 %s 的父级 %s 不存在，已按顶级分组导入", plan.id, *plan.parentID)
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
			warnf("分组 %s 的父级会在合并后形成循环，已按顶级分组导入", plan.id)
			plan.parentID = nil
			parents[plan.id] = nil
		case groupTopologyTooDeep:
			warnf("分组 %s 合并后的祖先链超过 64 层，已按顶级分组导入", plan.id)
			plan.parentID = nil
			parents[plan.id] = nil
		}
	}
	return plans, nil
}
