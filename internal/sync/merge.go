package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	// syncTranscriptMaxContentBytes: transcript records larger than this only sync metadata (matches the local 64MiB transcript limit).
	syncTranscriptMaxContentBytes = 64 << 20
	maxSyncWarnings               = 100
)

type localObject struct {
	kind      string
	plaintext []byte
}

// objectRevision returns the record's effective revision: the deletion time participates in the comparison, so deleting counts as one revision.
func objectRevision(updatedAt int64, deletedAt *int64) int64 {
	if deletedAt != nil && *deletedAt > updatedAt {
		return *deletedAt
	}
	return updatedAt
}

func localRevision(kind string, plaintext []byte) int64 {
	var payload struct {
		UpdatedAt int64  `json:"updatedAt"`
		DeletedAt *int64 `json:"deletedAt"`
		AddedAt   int64  `json:"addedAt"`
		EndedAt   int64  `json:"endedAt"`
	}
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return 0
	}
	var source revisionSource
	if spec, ok := lookupKind(kind); ok {
		source = spec.revision
	}
	switch source {
	case revisionEnded:
		return payload.EndedAt
	case revisionAdded:
		return payload.AddedAt
	case revisionDeleted:
		if payload.DeletedAt != nil {
			return *payload.DeletedAt
		}
		return 0
	}
	if payload.DeletedAt != nil && *payload.DeletedAt > payload.UpdatedAt {
		return *payload.DeletedAt
	}
	return payload.UpdatedAt
}

// remoteWins decides LWW: the higher revision wins; ties break by payload sha256 lexicographic order, so bidirectional sync converges without negotiation.
func remoteWins(remoteRevision, localRevision int64, remotePlaintext, localPlaintext []byte) bool {
	if remoteRevision != localRevision {
		return remoteRevision > localRevision
	}
	return bytes.Compare([]byte(objectPayloadHash(remotePlaintext)), []byte(objectPayloadHash(localPlaintext))) > 0
}

func (report *SyncReport) warnf(format string, args ...any) {
	if len(report.Warnings) >= maxSyncWarnings {
		return
	}
	report.Warnings = append(report.Warnings, fmt.Sprintf(format, args...))
}

// collectLocalObjects scans the full local replica and produces deterministic payloads; the local replica stays identical before and after login.
// Known hosts, AI profiles, AI permissions, AI grants, and preferences are collected per the session user's opt-in: when off, the corresponding objects and their tombstones are not collected
// (disabling sync must not delete remote objects).
func (e *Engine) collectLocalObjects(ctx context.Context, report *SyncReport, optIn kindOptIn, userIDs ...string) (map[string]localObject, error) {
	collection := &localCollection{objects: map[string]localObject{}, report: report, optIn: optIn, userID: firstUserID(userIDs)}
	for index := range syncKinds {
		spec := &syncKinds[index]
		if spec.collect == nil || !optIn.enabled(spec.optIn) || (spec.collectNeedsUser && collection.userID == "") {
			continue
		}
		if err := spec.collect(e, ctx, collection); err != nil {
			return nil, err
		}
	}
	// Tombstones always collect last: one replaces a live object only when it is newer.
	if err := e.collectTombstoneObjects(ctx, collection); err != nil {
		return nil, err
	}
	return collection.objects, nil
}

func (e *Engine) collectGroupObjects(ctx context.Context, c *localCollection) error {
	groups, err := e.store.GroupList(ctx)
	if err != nil {
		return err
	}
	for _, group := range groups {
		payload, err := marshalObject(groupObject{
			ID: group.ID, ParentID: group.ParentID, Name: group.Name, Sort: group.Sort,
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
		})
		if err != nil {
			return err
		}
		c.objects[group.ID] = localObject{KindGroup, payload}
	}
	return nil
}

func (e *Engine) collectCredentialObjects(ctx context.Context, c *localCollection) error {
	credentials, err := e.store.CredentialList(ctx)
	if err != nil {
		return err
	}
	if len(credentials) > 0 {
		// Credentials are not synced this round while the vault is locked/uninitialized (same deferred semantics as keyed AI profiles); other objects are collected as usual.
		// Credentials stay out of objects, so no reconciliation state is written; the next round after unlock pushes them automatically.
		if err := e.requireVault(ctx); err != nil {
			c.report.warnf("%d 条凭据因凭据库未解锁而跳过本次同步，其余对象正常同步：%v", len(credentials), err)
			credentials = nil
		}
	}
	for _, row := range credentials {
		secret, err := e.vault.DecryptCredentialString(ctx, row)
		if err != nil {
			c.report.warnf("凭据 %s 解密失败，本次不同步：%v", row.ID, err)
			continue
		}
		payload, err := marshalObject(credentialObject{
			ID: row.ID, Name: row.Name, Kind: row.Kind, Secret: secret, UpdatedAt: row.UpdatedAt,
		})
		if err != nil {
			return err
		}
		c.objects[row.ID] = localObject{KindCredential, payload}
	}
	return nil
}

func (e *Engine) collectSnippetObjects(ctx context.Context, c *localCollection) error {
	snippets, err := e.store.SnippetList(ctx)
	if err != nil {
		return err
	}
	for _, snippet := range snippets {
		payload, err := marshalObject(snippetObject{
			ID: snippet.ID, GroupID: snippet.GroupID, Name: snippet.Name, Body: snippet.Body,
			Sort: snippet.Sort, CreatedAt: snippet.CreatedAt, UpdatedAt: snippet.UpdatedAt,
		})
		if err != nil {
			return err
		}
		c.objects[snippet.ID] = localObject{KindSnippet, payload}
	}
	return nil
}

func (e *Engine) collectAssetObjects(ctx context.Context, c *localCollection) error {
	assets, err := e.store.AssetList(ctx, true)
	if err != nil {
		return err
	}
	for _, asset := range assets {
		if asset.Builtin || asset.ID == store.BuiltinLocalAssetID {
			continue
		}
		payload, err := marshalObject(assetObject{
			ID: asset.ID, GroupID: asset.GroupID, Kind: asset.Kind, Name: asset.Name,
			Host: asset.Host, Port: asset.Port, Username: asset.Username, AuthKind: asset.AuthKind,
			KeyPath: asset.KeyPath, CredID: asset.CredID, OptionsJSON: asset.OptionsJSON,
			Tags: asset.Tags, Note: asset.Note, Sort: asset.Sort, CreatedAt: asset.CreatedAt,
			UpdatedAt: asset.UpdatedAt, DeletedAt: asset.DeletedAt,
		})
		if err != nil {
			return err
		}
		c.objects[asset.ID] = localObject{KindAsset, payload}
	}
	return nil
}

func (e *Engine) collectTranscriptObjects(ctx context.Context, c *localCollection) error {
	transcripts, err := e.store.TranscriptListOptedIn(ctx)
	if err != nil {
		return err
	}
	for _, transcript := range transcripts {
		payload, err := e.transcriptPayload(ctx, transcript)
		if err != nil {
			c.report.warnf("会话记录 %s 打包失败，本次不同步：%v", transcript.ID, err)
			continue
		}
		c.objects[transcript.ID] = localObject{KindTranscript, payload}
	}
	return nil
}

func (e *Engine) collectKnownHostObjects(ctx context.Context, c *localCollection) error {
	knownHosts, err := e.store.KnownHostList(ctx)
	if err != nil {
		return err
	}
	for _, row := range knownHosts {
		payload, err := marshalObject(knownHostObject{
			ID: row.ID, Host: row.Host, Port: row.Port, KeyType: row.KeyType,
			Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
		})
		if err != nil {
			return err
		}
		c.objects[row.ID] = localObject{KindKnownHost, payload}
	}
	return nil
}

func (e *Engine) collectAIProfileObjects(ctx context.Context, c *localCollection) error {
	profileState, profileRevision, _, err := e.aiProfilesLoad(ctx)
	if err != nil {
		if !errors.Is(err, errAIProfilesCorrupt) {
			return err
		}
		c.report.warnf("AI 模型档案数据损坏，本次不同步：%v", err)
		return nil
	}
	for _, record := range profileState.Profiles {
		key, err := e.revealAIProfileKey(ctx, record.APIKey)
		if err != nil {
			c.report.warnf("AI 模型档案 %s 解密失败，本次不同步：%v", record.ID, err)
			continue
		}
		record.APIKey = key
		payload, err := marshalObject(aiProfilePayloadFromRecord(record, profileRevision))
		if err != nil {
			return err
		}
		c.objects[record.ID] = localObject{KindAIProfile, payload}
	}
	return nil
}

// collectTombstoneObjects adds deletion tombstones for opted-in target kinds; a live object newer than its tombstone is kept.
// Credential tombstones are stored separately and win same-ID conflicts with sync tombstones.
func (e *Engine) collectTombstoneObjects(ctx context.Context, c *localCollection) error {
	tombstones, err := e.syncTombstoneList(ctx, e.aiSharedAccess(ctx))
	if err != nil {
		return err
	}
	knownHostMetas, err := e.knownHostTombstoneMetas(ctx)
	if err != nil {
		return err
	}
	for _, tombstone := range tombstones {
		if !c.optIn.allowsKind(tombstone.Kind) {
			continue
		}
		if live, exists := c.objects[tombstone.ID]; exists && localRevision(live.kind, live.plaintext) > tombstone.DeletedAt {
			continue
		}
		object := tombstoneObject{TargetKind: tombstone.Kind, DeletedAt: tombstone.DeletedAt}
		if tombstone.Kind == KindKnownHost {
			if meta, found := knownHostMetas[tombstone.ID]; found {
				object.Host, object.Port, object.KeyType = meta.Host, meta.Port, meta.KeyType
			}
		}
		payload, err := marshalObject(object)
		if err != nil {
			return err
		}
		c.objects[tombstone.ID] = localObject{KindTombstone, payload}
	}
	credentialTombstones, err := e.store.CredentialTombstoneList(ctx)
	if err != nil {
		return err
	}
	for _, tombstone := range credentialTombstones {
		if live, exists := c.objects[tombstone.ID]; exists && localRevision(live.kind, live.plaintext) > tombstone.DeletedAt {
			continue
		}
		payload, err := marshalObject(tombstoneObject{TargetKind: KindCredential, DeletedAt: tombstone.DeletedAt})
		if err != nil {
			return err
		}
		c.objects[tombstone.ID] = localObject{KindTombstone, payload}
	}
	return nil
}

func transcriptCacheKeyFor(ctx context.Context, transcript store.TranscriptRow) transcriptCacheKey {
	key := transcriptCacheKey{
		owner: ipc.OwnerID(ctx), id: transcript.ID, sessionID: transcript.SessionID,
		assetID: transcript.AssetID, assetName: transcript.AssetName, assetKind: transcript.AssetKind,
		startedAt: transcript.StartedAt, bytes: transcript.Bytes, chunks: transcript.Chunks,
		truncated: transcript.Truncated, contentOmitted: transcript.ContentOmitted,
	}
	if transcript.EndedAt != nil {
		key.hasEndedAt = true
		key.endedAt = *transcript.EndedAt
	}
	return key
}

func transcriptCacheID(key transcriptCacheKey) string {
	return key.owner + "\x00" + key.id
}

func (e *Engine) transcriptPayload(ctx context.Context, transcript store.TranscriptRow) ([]byte, error) {
	key := transcriptCacheKeyFor(ctx, transcript)
	cacheID := transcriptCacheID(key)
	e.transcriptCacheMu.RLock()
	cached, found := e.transcriptCache[cacheID]
	e.transcriptCacheMu.RUnlock()
	if found && cached.key == key {
		return cached.payload, nil
	}

	object := transcriptObject{
		ID: transcript.ID, SessionID: transcript.SessionID, AssetID: transcript.AssetID,
		AssetName: transcript.AssetName, AssetKind: transcript.AssetKind,
		StartedAt: transcript.StartedAt, Truncated: transcript.Truncated,
		Bytes: transcript.Bytes, Chunks: transcript.Chunks,
	}
	if transcript.EndedAt != nil {
		object.EndedAt = *transcript.EndedAt
	}
	var payload []byte
	var err error
	if transcript.Bytes > syncTranscriptMaxContentBytes {
		object.ContentOmitted = true
		payload, err = marshalObject(object)
	} else {
		chunks, readErr := e.transcriptChunksAll(ctx, transcript.ID)
		if readErr != nil {
			return nil, readErr
		}
		object.Content = make([]transcriptChunkObject, 0, len(chunks))
		for _, chunk := range chunks {
			object.Content = append(object.Content, transcriptChunkObject{
				Seq: chunk.Seq, TabID: chunk.TabID, TS: chunk.TS, Kind: chunk.Kind, Data: chunk.Data,
			})
		}
		payload, err = marshalObject(object)
	}
	if err != nil {
		return nil, err
	}
	e.transcriptCacheMu.Lock()
	e.transcriptCache[cacheID] = transcriptCacheEntry{key: key, payload: payload, payloadHash: objectPayloadHash(payload)}
	e.transcriptCacheMu.Unlock()
	return payload, nil
}

// localPayloadHash reuses the hash stored beside an immutable transcript payload. The byte comparison
// protects against a concurrent cache replacement; all other object kinds retain the existing hash path.
func (e *Engine) localPayloadHash(ctx context.Context, id string, object localObject) string {
	if object.kind == KindTranscript {
		cacheID := ipc.OwnerID(ctx) + "\x00" + id
		e.transcriptCacheMu.RLock()
		cached, found := e.transcriptCache[cacheID]
		e.transcriptCacheMu.RUnlock()
		if found && bytes.Equal(cached.payload, object.plaintext) {
			return cached.payloadHash
		}
	}
	return objectPayloadHash(object.plaintext)
}

func (e *Engine) transcriptChunksAll(ctx context.Context, transcriptID string) ([]store.TranscriptChunkRow, error) {
	chunks := []store.TranscriptChunkRow{}
	afterSeq := int64(0)
	for {
		page, err := e.store.TranscriptChunks(ctx, transcriptID, afterSeq, 8<<20)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return chunks, nil
		}
		chunks = append(chunks, page...)
		afterSeq = page[len(page)-1].Seq + 1
	}
}

// applyRemoteObject decrypts and LWW-applies one remote object; apply failures are isolated as warnings and do not block the whole sync round.
// identical means remote and local content match, counted as reconciled; when local wins, payload_hash is left empty so the blob hash difference drives the re-push.
// known_host/AI profiles (including their tombstones) with opt-in hard-off are only counted and skipped: not applied and not reconciled, staying unreconciled for a later re-pull once enabled.
func (e *Engine) applyRemoteObject(ctx context.Context, userID string, object WireObjectSeq, dek []byte, report *SyncReport, optIn kindOptIn) error {
	report.Pulled++
	kind, plaintext, err := tryOpenObject(dek, object.Blob, object.ID)
	if err != nil {
		report.DecryptFailed++
		report.warnf("对象 %s 解密失败（密钥不匹配或数据损坏），已隔离", object.ID)
		return e.quarantineObject(ctx, userID, object.ID, hashBlob(object.Blob))
	}
	if !optIn.allowsRemote(kind, plaintext) {
		report.PullSkipped++
		return nil
	}
	// Applying credentials requires an unlocked vault: while locked, neither apply nor reconcile (same semantics as opt-in hard-off),
	// staying unreconciled so the next round after unlock re-pulls and applies automatically; recording the blob hash would be mistaken for convergence and skip forever.
	if kind == KindCredential {
		if err := e.requireVault(ctx); err != nil {
			report.PullSkipped++
			report.leftPending = true
			report.warnf("凭据 %s 暂缓应用：凭据库未解锁，解锁后下一轮同步自动重试", object.ID)
			return nil
		}
	}
	payloadHash := objectPayloadHash(plaintext)
	applied, identical := e.applyDecryptedObject(ctx, object.ID, kind, plaintext, report, userID)
	report.PullSkipped++
	if applied {
		report.Applied++
		report.PullSkipped--
	}
	switch {
	case report.hasUnresolvedRef(object.ID):
		// Reference-degraded apply: not reconciled; the next round re-pulls and recovers once the dependency arrives.
	case applied || identical:
		e.syncStatePut(ctx, userID, object.ID, &payloadHash, hashBlob(object.Blob))
	default:
		e.syncStatePut(ctx, userID, object.ID, nil, hashBlob(object.Blob))
	}
	return nil
}

// quarantineObject records the blob hash of an undecryptable object to avoid re-pulling it every round and prevent an automatic local push from overwriting it.
func (e *Engine) quarantineObject(ctx context.Context, userID, objectID string, blobHash [32]byte) error {
	payloadHash := quarantinedPayloadHash
	if err := e.syncStatePut(ctx, userID, objectID, &payloadHash, blobHash); err != nil {
		e.logger.Warn("sync quarantine record failed", "error", err)
		return err
	}
	return nil
}

func (e *Engine) applyGroupObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload groupObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("分组对象数据损坏：%v", err)
		return false, false
	}
	local, err := e.store.GroupGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查分组 %s: %v", payload.ID, err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查分组 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	parentID := payload.ParentID
	unresolvedParent := false
	if parentID != nil {
		if *parentID == "" {
			parentID = nil
		} else if _, err := e.store.GroupGet(ctx, *parentID); isNotFound(err) {
			parentID = nil
			deleted, found, tombstoneErr := e.syncTombstoneGet(ctx, *payload.ParentID)
			if tombstoneErr != nil {
				report.warnf("无法检查分组 %s 的父级删除标记: %v", payload.ID, tombstoneErr)
				return false, false
			}
			unresolvedParent = !found || deleted.Kind != KindGroup || deleted.DeletedAt < payload.UpdatedAt
			if unresolvedParent {
				report.warnf("分组 %s 的父级 %s 尚未同步，暂时按顶级分组导入，父级到达后自动恢复", payload.ID, *payload.ParentID)
			} else {
				report.warnf("分组 %s 的父级 %s 已删除，按顶级分组导入", payload.ID, *payload.ParentID)
			}
		} else if err != nil {
			report.warnf("无法检查分组 %s 的父级: %v", payload.ID, err)
			return false, false
		}
	}
	if exists {
		localPayload, err := marshalObject(groupObject{
			ID: local.ID, ParentID: local.ParentID, Name: local.Name, Sort: local.Sort,
			CreatedAt: local.CreatedAt, UpdatedAt: local.UpdatedAt,
		})
		if err != nil {
			report.warnf("分组 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			if unresolvedParent {
				report.markUnresolvedRef(payload.ID)
			}
			return false, true
		}
		degraded := payload
		degraded.ParentID = nil
		degradedPayload, err := marshalObject(degraded)
		if err != nil {
			report.warnf("分组 %s 的兼容格式编码失败：%v", payload.ID, err)
			return false, false
		}
		if !bytes.Equal(degradedPayload, localPayload) && !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPayload) {
			return false, false
		}
	}
	if parentID != nil {
		parents, err := e.groupParents(ctx)
		if err != nil {
			report.warnf("无法读取本机分组拓扑: %v", err)
			return false, false
		}
		switch mergedGroupTopology(payload.ID, parentID, parents) {
		case groupTopologyCycle:
			report.warnf("分组 %s 的父级会在合并后形成循环，已按顶级分组导入", payload.ID)
			parentID = nil
		case groupTopologyTooDeep:
			report.warnf("分组 %s 合并后的祖先链超过 64 层，已按顶级分组导入", payload.ID)
			parentID = nil
		}
	}
	var snapshot *store.AssetGroupRow
	if exists {
		snapshot = &local
	}
	if err := e.groupUpsert(ctx, payload, parentID, snapshot); err != nil {
		report.warnf("分组 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	if unresolvedParent {
		report.markUnresolvedRef(payload.ID)
	}
	return true, false
}

// groupUpsert mirrors store.GroupUpsert semantics and clears the same-ID deletion tombstone in the same transaction:
// once a newer group beats an old tombstone, no tombstone that could roll back the object may remain.
// The write binds to the snapshot the LWW decision was made on (nil for a group that did not exist locally).
func (e *Engine) groupUpsert(ctx context.Context, payload groupObject, parentID *string, snapshot *store.AssetGroupRow) error {
	if err := store.EnsureID(payload.ID); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Name) == "" {
		return ipc.NewError(ipc.CodeBadParam, "分组名称不能为空")
	}
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return e.groupUpsertTxBound(ctx, tx, payload, parentID, snapshot)
	})
}

// groupUpsertTxBound writes one group inside a transaction with the write bound to the
// caller's snapshot: the planner's equal-revision winner lands while a concurrent local
// write is never overwritten (the conflict leaves the object unreconciled for the next
// round). The explicit bundle import passes the pre-import snapshot, and its second
// topology pass passes the first pass's written content.
func (e *Engine) groupUpsertTxBound(ctx context.Context, tx *gorm.DB, payload groupObject, parentID *string, snapshot *store.AssetGroupRow) error {
	row := &store.AssetGroupRow{ID: payload.ID, ParentID: parentID, Name: strings.TrimSpace(payload.Name), Sort: payload.Sort, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt, OwnerID: ipc.OwnerID(ctx)}
	if snapshot == nil {
		if err := store.UpsertOwnerGuardedExpected(ctx, tx, row, []string{"parent_id", "name", "sort", "updated_at"}, 0); err != nil {
			return err
		}
	} else {
		if _, err := store.UpsertOwnerGuardedExpectedContent(ctx, tx, row, []string{"parent_id", "name", "sort", "updated_at"}, snapshot); err != nil {
			return err
		}
	}
	tombstone := query.Use(tx).SyncTombstone
	if _, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(payload.ID)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func (e *Engine) groupParents(ctx context.Context) (map[string]*string, error) {
	groups, err := e.store.GroupList(ctx)
	if err != nil {
		return nil, err
	}
	parents := make(map[string]*string, len(groups))
	for _, group := range groups {
		parents[group.ID] = group.ParentID
	}
	return parents, nil
}

func (e *Engine) applyAssetObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload assetObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("资产对象数据损坏：%v", err)
		return false, false
	}
	if payload.ID == store.BuiltinLocalAssetID {
		report.warnf("内置\"当前设备\"不接受同步覆盖")
		return false, false
	}
	local, err := e.store.AssetGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查资产 %s: %v", payload.ID, err)
		return false, false
	}
	if exists {
		if local.Builtin {
			return false, false
		}
		localPayload, err := marshalObject(assetObject{
			ID: local.ID, GroupID: local.GroupID, Kind: local.Kind, Name: local.Name,
			Host: local.Host, Port: local.Port, Username: local.Username, AuthKind: local.AuthKind,
			KeyPath: local.KeyPath, CredID: local.CredID, OptionsJSON: local.OptionsJSON,
			Tags: local.Tags, Note: local.Note, Sort: local.Sort, CreatedAt: local.CreatedAt,
			UpdatedAt: local.UpdatedAt, DeletedAt: local.DeletedAt,
		})
		if err != nil {
			report.warnf("资产 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		// Local is the reference-degraded form of the remote payload (reference cleared earlier because the credential had not arrived): if the credential is still missing, leave everything untouched and stay unreconciled,
		// re-deciding on the next round's re-pull; if it has arrived, bypass LWW and restore directly - the degraded form is not a user edit and does not participate in the revision contest.
		degraded := payload
		degraded.CredID = nil
		degradedPayload, err := marshalObject(degraded)
		if err != nil {
			report.warnf("资产 %s 的兼容格式编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(degradedPayload, localPayload) {
			if _, err := e.store.CredentialGetRow(ctx, *payload.CredID); isNotFound(err) {
				report.markUnresolvedRef(payload.ID)
				return false, false
			} else if err != nil {
				report.warnf("无法检查资产 %s 的凭据: %v", payload.ID, err)
				return false, false
			}
		} else if !remoteWins(objectRevision(payload.UpdatedAt, payload.DeletedAt), objectRevision(local.UpdatedAt, local.DeletedAt), plaintext, localPayload) {
			return false, false
		}
	}
	row := store.AssetRow{
		ID: payload.ID, GroupID: payload.GroupID, Kind: payload.Kind, Name: payload.Name,
		Host: payload.Host, Port: payload.Port, Username: payload.Username, AuthKind: payload.AuthKind,
		KeyPath: payload.KeyPath, CredID: payload.CredID, OptionsJSON: payload.OptionsJSON,
		Tags: payload.Tags, Note: payload.Note, Sort: payload.Sort, CreatedAt: payload.CreatedAt,
		UpdatedAt: payload.UpdatedAt, DeletedAt: payload.DeletedAt,
	}
	if row.GroupID != nil {
		if *row.GroupID == "" {
			row.GroupID = nil
		} else if _, err := e.store.GroupGet(ctx, *row.GroupID); isNotFound(err) {
			report.warnf("资产 %s 引用的分组 %s 不存在，已清除该引用", payload.ID, *row.GroupID)
			row.GroupID = nil
		} else if err != nil {
			report.warnf("无法检查资产 %s 的分组: %v", payload.ID, err)
			return false, false
		}
	}
	if row.CredID != nil {
		if *row.CredID == "" {
			row.CredID = nil
		} else if _, err := e.store.CredentialGetRow(ctx, *row.CredID); isNotFound(err) {
			// cred_id has an FK constraint (OnDelete:SET NULL); a dangling reference cannot be written to the database and can only be cleared by degradation;
			// after marking the unresolved reference the engine does not reconcile, and the next round's re-pull restores the reference automatically via the degraded-form check once the credential arrives.
			report.warnf("资产 %s 引用的凭据 %s 尚未同步到本机，已暂时清除该引用，凭据同步后自动恢复", payload.ID, *row.CredID)
			row.CredID = nil
			report.markUnresolvedRef(payload.ID)
		} else if err != nil {
			report.warnf("无法检查资产 %s 的凭据: %v", payload.ID, err)
			return false, false
		}
	}
	// The LWW decision above was made against the local snapshot; the guarded upsert
	// lands only when that snapshot is still current, so a concurrent reconciler or
	// user write is never silently overwritten. A conflict leaves the object
	// unreconciled: the next round re-pulls and re-decides on the fresh state.
	var base *store.AssetRow
	if exists {
		base = &local
	}
	if _, err := e.store.AssetUpsertGuarded(ctx, row, base); err != nil {
		if errors.Is(err, store.ErrWriteConflict) {
			report.warnf("资产 %s 在本机已被更新，本次未覆盖，将在下一轮同步重新合并", payload.ID)
			return false, false
		}
		report.warnf("资产 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyCredentialObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload credentialObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("凭据对象数据损坏：%v", err)
		return false, false
	}
	if err := e.requireVault(ctx); err != nil {
		report.warnf("凭据 %s 需要解锁凭据库才能应用: %v", payload.ID, err)
		return false, false
	}
	local, err := e.store.CredentialGetRow(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查凭据 %s: %v", payload.ID, err)
		return false, false
	}
	if tombstone, err := e.store.CredentialTombstoneGet(ctx, payload.ID); err == nil && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	} else if err != nil && !isNotFound(err) {
		report.warnf("无法检查凭据 %s 的删除标记：%v", payload.ID, err)
		return false, false
	}
	if exists {
		secret, err := e.vault.DecryptCredentialString(ctx, local)
		if err != nil {
			report.warnf("凭据 %s 本地解密失败: %v", payload.ID, err)
			return false, false
		}
		localPayload, err := marshalObject(credentialObject{
			ID: local.ID, Name: local.Name, Kind: local.Kind, Secret: secret, UpdatedAt: local.UpdatedAt,
		})
		if err != nil {
			report.warnf("凭据 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPayload) {
			return false, false
		}
	}
	nonce, blob, err := e.vault.EncryptCredential(ctx, payload.Secret)
	if err != nil {
		report.warnf("凭据 %s 重加密失败: %v", payload.ID, err)
		return false, false
	}
	var snapshot *store.CredentialRow
	if exists {
		snapshot = &local
	}
	if err := e.credentialUpsert(ctx, payload, nonce, blob, snapshot); err != nil {
		report.warnf("凭据 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

// credentialUpsert mirrors store.CredentialPut but preserves the peer revision: the store layer writes updated_at with the local clock,
// while sync payload hashing goes by the revision, which must be preserved verbatim for idempotent reconciliation.
// The write binds to the snapshot the LWW decision was made on (nil for a credential that did not exist locally):
// the planner's equal-revision winner lands only while that snapshot is still current, and a concurrent local
// write is never overwritten — the conflict leaves the object unreconciled for the next round to re-decide.
// The write and the deletion-tombstone clearing share one transaction: once a newer credential beats an old tombstone, no tombstone that could roll back the object may remain.
func (e *Engine) credentialUpsert(ctx context.Context, payload credentialObject, nonce, blob []byte, snapshot *store.CredentialRow) error {
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := e.credentialUpsertTxBound(ctx, tx, payload, nonce, blob, snapshot)
		return err
	})
}

// credentialUpsertTxBound is the snapshot-bound credential write: an existing row is
// updated only when its content still equals the snapshot's, and a new row is inserted
// only when it is still absent. The explicit bundle import passes the pre-import snapshot
// so a later local credential write is never blindly overwritten. The boolean result
// reports whether the write actually landed; a false nil-error result is a genuine
// idempotent replay (the snapshot already carried the target content), which the caller
// must not record as a write of its own.
func (e *Engine) credentialUpsertTxBound(ctx context.Context, tx *gorm.DB, payload credentialObject, nonce, blob []byte, snapshot *store.CredentialRow) (bool, error) {
	row := &store.CredentialRow{ID: payload.ID, Name: payload.Name, Kind: payload.Kind, Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob, KEKHint: e.vault.KEKHint(), CreatedAt: payload.UpdatedAt, UpdatedAt: payload.UpdatedAt, OwnerID: ipc.OwnerID(ctx)}
	landed := true
	if snapshot == nil {
		if err := store.UpsertOwnerGuardedExpected(ctx, tx, row, []string{"name", "kind", "cipher", "nonce", "blob", "kek_hint", "updated_at"}, 0); err != nil {
			return false, err
		}
	} else {
		var err error
		if landed, err = store.UpsertOwnerGuardedExpectedContent(ctx, tx, row, []string{"name", "kind", "cipher", "nonce", "blob", "kek_hint", "updated_at"}, snapshot); err != nil {
			return false, err
		}
	}
	tombstone := query.Use(tx).CredentialTombstone
	if _, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(payload.ID)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete(); err != nil {
		return false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return landed, nil
}

func (e *Engine) applySnippetObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload snippetObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("片段对象数据损坏：%v", err)
		return false, false
	}
	local, err := e.store.SnippetGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查片段 %s: %v", payload.ID, err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查片段 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	if exists {
		localPayload, err := marshalObject(snippetObject{
			ID: local.ID, GroupID: local.GroupID, Name: local.Name, Body: local.Body,
			Sort: local.Sort, CreatedAt: local.CreatedAt, UpdatedAt: local.UpdatedAt,
		})
		if err != nil {
			report.warnf("片段 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPayload) {
			return false, false
		}
	}
	row := store.SnippetRow{
		ID: payload.ID, GroupID: payload.GroupID, Name: payload.Name, Body: payload.Body,
		Sort: payload.Sort, CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt,
	}
	if row.GroupID != nil {
		if *row.GroupID == "" {
			row.GroupID = nil
		} else if _, err := e.store.GroupGet(ctx, *row.GroupID); isNotFound(err) {
			report.warnf("片段 %s 引用的分组 %s 不存在，已清除该引用", payload.ID, *row.GroupID)
			row.GroupID = nil
		} else if err != nil {
			report.warnf("无法检查片段 %s 的分组: %v", payload.ID, err)
			return false, false
		}
	}
	var snapshot *store.SnippetRow
	if exists {
		snapshot = &local
	}
	if err := e.snippetUpsert(ctx, row, snapshot); err != nil {
		report.warnf("片段 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyTombstoneObject(ctx context.Context, objectID string, plaintext []byte, report *SyncReport, userID string) (bool, bool) {
	var payload tombstoneObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("删除标记对象数据损坏：%v", err)
		return false, false
	}
	target, ok := lookupKind(payload.TargetKind)
	if !ok || target.applyTombstone == nil {
		report.warnf("删除标记对象目标种类 %s 不受支持", payload.TargetKind)
		return false, false
	}
	applied, identical := target.applyTombstone(e, ctx, objectID, payload, report, userID)
	if applied {
		report.markChanged(target.kind)
	}
	return applied, identical
}

func (e *Engine) applyGroupTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	local, err := e.store.GroupGet(ctx, objectID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查分组 %s: %v", objectID, err)
		return false, false
	}
	if exists && local.UpdatedAt > deletedAt {
		return false, false
	}
	if exists {
		if err := e.store.GroupDeleteAt(ctx, objectID, deletedAt); err != nil {
			report.warnf("应用分组 %s 的删除标记失败：%v", objectID, err)
			return false, false
		}
	} else if err := e.syncTombstonePut(ctx, objectID, KindGroup, deletedAt); err != nil {
		report.warnf("分组 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applySnippetTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	local, err := e.store.SnippetGet(ctx, objectID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查片段 %s: %v", objectID, err)
		return false, false
	}
	if exists && local.UpdatedAt > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindSnippet, deletedAt); err != nil {
		report.warnf("片段 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	if exists {
		if err := e.store.SnippetDelete(ctx, objectID); err != nil {
			report.warnf("应用片段 %s 的删除标记失败：%v", objectID, err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyCredentialTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	local, err := e.store.CredentialGetRow(ctx, objectID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查凭据 %s: %v", objectID, err)
		return false, false
	}
	if exists && local.UpdatedAt > deletedAt {
		return false, false
	}
	if exists {
		if err := e.store.CredentialDeleteRow(ctx, objectID); err != nil {
			report.warnf("应用凭据 %s 的删除标记失败：%v", objectID, err)
			return false, false
		}
	}
	if err := e.store.CredentialTombstonePut(ctx, objectID, deletedAt); err != nil {
		report.warnf("凭据 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyTranscriptTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	local, err := e.store.TranscriptGet(ctx, objectID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查会话记录 %s: %v", objectID, err)
		return false, false
	}
	if exists && !local.SyncOptIn {
		// Local opt-out record is kept: treated as reconciled with the remote tombstone; the local copy does not participate in pushes.
		return false, true
	}
	if err := e.syncTombstonePut(ctx, objectID, KindTranscript, deletedAt); err != nil {
		report.warnf("会话记录 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	if exists {
		transcript := query.Use(e.store.GORM()).TranscriptRow
		if _, err := transcript.WithContext(ctx).
			Where(transcript.ID.Eq(objectID)).
			Where(ownerConditions(ctx, transcript.TableName())...).
			Delete(); err != nil {
			report.warnf("应用会话记录 %s 的删除标记失败：%v", objectID, err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyTranscriptObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload transcriptObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("会话记录对象数据损坏：%v", err)
		return false, false
	}
	if payload.EndedAt <= 0 {
		report.warnf("会话记录 %s 未结束，不同步", payload.ID)
		return false, false
	}
	if _, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查会话记录 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found {
		return false, false
	}
	local, err := e.store.TranscriptGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查会话记录 %s: %v", payload.ID, err)
		return false, false
	}
	if exists {
		if local.ContentOmitted && !payload.ContentOmitted && len(payload.Content) > 0 {
			if err := e.upgradeTranscriptContent(ctx, local.ID, payload); err != nil {
				report.warnf("会话记录 %s 内容补全失败: %v", payload.ID, err)
				return false, false
			}
			return true, false
		}
		// Transcript content is immutable: an existing local copy counts as identical.
		return false, true
	}
	row := store.TranscriptRow{
		ID: payload.ID, SessionID: payload.SessionID, AssetID: payload.AssetID,
		AssetName: payload.AssetName, AssetKind: payload.AssetKind,
		StartedAt: payload.StartedAt, EndedAt: &payload.EndedAt,
		Bytes: payload.Bytes, Chunks: payload.Chunks, Truncated: payload.Truncated,
		ContentOmitted: payload.ContentOmitted,
	}
	chunks := make([]store.TranscriptChunkRow, 0, len(payload.Content))
	for _, chunk := range payload.Content {
		chunks = append(chunks, store.TranscriptChunkRow{Seq: chunk.Seq, TabID: chunk.TabID, TS: chunk.TS, Kind: chunk.Kind, Data: chunk.Data})
	}
	if err := e.store.TranscriptInsertSynced(ctx, row, chunks); err != nil {
		report.warnf("会话记录 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) upgradeTranscriptContent(ctx context.Context, transcriptID string, payload transcriptObject) error {
	return e.store.TranscriptReplaceContent(ctx, transcriptID, payload.Bytes, payload.Chunks, payload.Truncated, chunksToRows(payload.Content))
}

func chunksToRows(chunks []transcriptChunkObject) []store.TranscriptChunkRow {
	rows := make([]store.TranscriptChunkRow, 0, len(chunks))
	for _, chunk := range chunks {
		rows = append(rows, store.TranscriptChunkRow{Seq: chunk.Seq, TabID: chunk.TabID, TS: chunk.TS, Kind: chunk.Kind, Data: chunk.Data})
	}
	return rows
}

func (e *Engine) applyKnownHostObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload knownHostObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("已知主机对象数据损坏：%v", err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查已知主机 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.AddedAt {
		return false, false
	}
	local, exists, err := e.knownHostByID(ctx, payload.ID)
	if err != nil {
		report.warnf("无法检查已知主机 %s: %v", payload.ID, err)
		return false, false
	}
	if exists {
		localPayload, err := marshalObject(knownHostObject{
			ID: local.ID, Host: local.Host, Port: local.Port, KeyType: local.KeyType,
			Fingerprint: local.Fingerprint, AddedAt: local.AddedAt,
		})
		if err != nil {
			report.warnf("已知主机 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.AddedAt, local.AddedAt, plaintext, localPayload) {
			return false, false
		}
	}
	// Only one row may exist per (host, port, keyType): same-triple rows with different IDs are decided by LWW,
	// the loser row is deleted and tombstoned at the winner's revision, so multiple devices converge on one object per host.
	displacedID := ""
	if conflict, found, err := e.store.KnownHostGet(ctx, payload.Host, payload.Port, payload.KeyType); err != nil {
		report.warnf("无法检查已知主机 %s 的主机、端口和密钥类型组合冲突：%v", payload.ID, err)
		return false, false
	} else if found && conflict.ID != payload.ID {
		conflictPayload, err := marshalObject(knownHostObject{
			ID: conflict.ID, Host: conflict.Host, Port: conflict.Port, KeyType: conflict.KeyType,
			Fingerprint: conflict.Fingerprint, AddedAt: conflict.AddedAt,
		})
		if err != nil {
			report.warnf("已知主机 %s 冲突数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if !remoteWins(payload.AddedAt, conflict.AddedAt, plaintext, conflictPayload) {
			// The local winner keeps the triple, but the remote loser object still lives on the server: tombstone the loser at the winner's revision so this
			// round's collect/push overwrites it with the tombstone, otherwise the old fingerprint would revive on new devices when the winner is deleted later.
			// A local live row with the same ID (an incarnation with a different triple) cannot coexist with the loser tombstone; yield the original ID before tombstoning.
			var incarnation *store.KnownHostRow
			if exists {
				incarnation = &local
			}
			if err := e.knownHostTombstoneLoser(ctx, payload, incarnation, conflict.AddedAt); err != nil {
				report.warnf("已知主机冲突项 %s 的删除标记记录失败：%v", payload.ID, err)
				return false, false
			}
			return false, false
		}
		displacedID = conflict.ID
	}
	var snapshot *store.KnownHostRow
	if exists {
		snapshot = &local
	}
	if err := e.knownHostUpsert(ctx, payload, displacedID, snapshot); err != nil {
		report.warnf("已知主机 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

// applyKnownHostTombstone distinguishes conflict tombstones from user-initiated deletions: a conflict tombstone carries the original loser triple,
// and a local row with the same ID but a different triple whose revision is not newer is the winning incarnation, which must not be cleared as a user
// deletion - it is safely re-IDed and propagated with this round's collect/push, while the conflict tombstone only clears the original slot.
// Same-triple loser rows and user deletions are cleared by ID.
func (e *Engine) applyKnownHostTombstone(ctx context.Context, objectID string, payload tombstoneObject, report *SyncReport) (bool, bool) {
	local, exists, err := e.knownHostByID(ctx, objectID)
	if err != nil {
		report.warnf("无法检查已知主机 %s: %v", objectID, err)
		return false, false
	}
	if exists && local.AddedAt > payload.DeletedAt {
		return false, false
	}
	conflict := payload.Host != "" || payload.KeyType != ""
	if exists && conflict && (local.Host != payload.Host || local.Port != payload.Port || local.KeyType != payload.KeyType) {
		newID, err := e.knownHostReincarnationID(&local)
		if err != nil {
			report.warnf("已知主机冲突项 %s 重新分配 ID 失败：%v", objectID, err)
			return false, false
		}
		err = e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			knownHost := query.Use(tx).KnownHostRow
			if _, err := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(local.ID)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				UpdateColumns(map[string]any{"id": newID}); err != nil {
				return err
			}
			return e.knownHostTombstoneRecordTx(ctx, tx, objectID, payload.DeletedAt, &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType})
		})
		if err != nil {
			report.warnf("已知主机 %s 的冲突项删除标记记录失败：%v", objectID, err)
			return false, false
		}
		return true, false
	}
	err = e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if exists {
			knownHost := query.Use(tx).KnownHostRow
			if _, err := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(objectID)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				Delete(); err != nil {
				return err
			}
		}
		var meta *knownHostTombstoneMeta
		if conflict {
			meta = &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType}
		}
		return e.knownHostTombstoneRecordTx(ctx, tx, objectID, payload.DeletedAt, meta)
	})
	if err != nil {
		report.warnf("已知主机 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIProfileObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 模型档案应用需要账号身份: %v", err)
		return false, false
	}
	var payload aiProfileObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 模型档案对象数据损坏：%v", err)
		return false, false
	}
	if payload.APIKey != "" {
		if err := e.requireVault(ctx); err != nil {
			report.warnf("AI 模型档案 %s 需要解锁凭据库才能应用: %v", payload.ID, err)
			return false, false
		}
	}
	state, revision, _, err := e.aiProfilesLoad(ctx)
	if err != nil {
		report.warnf("无法读取 AI 模型档案 %s: %v", payload.ID, err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查 AI 模型档案 %s 的删除标记：%v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	local, exists := state.find(payload.ID)
	if exists {
		localKey, err := e.revealAIProfileKey(ctx, local.APIKey)
		if err != nil {
			report.warnf("AI 模型档案 %s 本地解密失败: %v", payload.ID, err)
			return false, false
		}
		local.APIKey = localKey
		localPayload, err := marshalObject(aiProfilePayloadFromRecord(local, revision))
		if err != nil {
			report.warnf("AI 模型档案 %s 本机数据编码失败：%v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, revision, plaintext, localPayload) {
			return false, false
		}
	}
	record := aiProfileRecordFromPayload(payload)
	if record.APIKey != "" {
		envelope, err := e.protectAIProfileKey(ctx, record.APIKey)
		if err != nil {
			report.warnf("AI 模型档案 %s 重加密失败: %v", payload.ID, err)
			return false, false
		}
		record.APIKey = envelope
	}
	state.upsert(record)
	state.ensureActive()
	// Profiles share one LWW clock: revisions only increase, and newer local edits must not be rolled back by older remote objects.
	if payload.UpdatedAt > revision {
		revision = payload.UpdatedAt
	}
	if err := e.aiProfilesSave(ctx, state, revision, payload.ID); err != nil {
		report.warnf("AI 模型档案 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIProfileTombstone(ctx context.Context, objectID string, deletedAt int64, report *SyncReport) (bool, bool) {
	if _, err := e.aiOwnerID(ctx); err != nil {
		report.warnf("AI 模型档案删除标记需要账号身份: %v", err)
		return false, false
	}
	state, revision, _, err := e.aiProfilesLoad(ctx)
	if err != nil {
		report.warnf("无法读取 AI 模型档案 %s: %v", objectID, err)
		return false, false
	}
	_, exists := state.find(objectID)
	if exists && revision > deletedAt {
		return false, false
	}
	if err := e.syncTombstonePut(ctx, objectID, KindAIProfile, deletedAt); err != nil {
		report.warnf("AI 模型档案 %s 的删除标记记录失败：%v", objectID, err)
		return false, false
	}
	if exists {
		state.remove(objectID)
		state.ensureActive()
		if err := e.aiProfilesSave(ctx, state, revision, ""); err != nil {
			report.warnf("应用 AI 模型档案 %s 的删除标记失败：%v", objectID, err)
			return false, false
		}
	}
	return true, false
}

func unmarshalObject(plaintext []byte, dst any) error {
	if err := json.Unmarshal(plaintext, dst); err != nil {
		return ipc.WrapError(ipc.CodeBadParam, "同步对象数据不是合法 JSON", err)
	}
	return nil
}

func (e *Engine) requireVault(ctx context.Context) error {
	if e.vault == nil {
		return ipc.NewError(ipc.CodeVaultLocked, "凭据库不可用，无法同步凭据")
	}
	status := e.vault.Status()
	if !status.Initialized {
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库未初始化，无法同步凭据")
	}
	if !status.Unlocked {
		return ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return nil
}

func sortObjectsForPush(objects map[string]localObject) []string {
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := objects[ids[i]], objects[ids[j]]
		if left.kind != right.kind {
			return objectKindRank(left.kind) < objectKindRank(right.kind)
		}
		return ids[i] < ids[j]
	})
	ordered := make([]string, 0, len(ids))
	state := make(map[string]uint8, len(ids))
	var visit func(string)
	visit = func(id string) {
		if state[id] != 0 {
			return
		}
		state[id] = 1
		object := objects[id]
		if object.kind == KindGroup {
			var group groupObject
			if json.Unmarshal(object.plaintext, &group) == nil && group.ParentID != nil {
				if parent, ok := objects[*group.ParentID]; ok && parent.kind == KindGroup {
					visit(*group.ParentID)
				}
			}
		}
		state[id] = 2
		ordered = append(ordered, id)
	}
	for _, id := range ids {
		visit(id)
	}
	return ordered
}
