package sync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const (
	// syncTranscriptMaxContentBytes 超过该大小的会话记录只同步元数据(与本地 64MiB 会话上限一致)。
	syncTranscriptMaxContentBytes = 64 << 20
	maxSyncWarnings               = 100
)

type localObject struct {
	kind      string
	plaintext []byte
}

// objectRevision 返回记录的有效修订号: 删除时间参与比较, 删除即一次修订。
func objectRevision(updatedAt int64, deletedAt *int64) int64 {
	if deletedAt != nil && *deletedAt > updatedAt {
		return *deletedAt
	}
	return updatedAt
}

// remoteWins LWW 裁决: 修订号大者胜; 平手按载荷 sha256 字典序决胜, 双向同步无需协商即可收敛。
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

// collectLocalObjects 全量扫描本地副本产出确定性载荷; 本地副本未登录与登录后保持一致。
// known_host/AI 模型档案按会话用户 opt-in 收集(默认关): 未开启时不收集这两类对象, 也不传播其墓碑
// (禁用同步不得删除远端对象)。
func (e *Engine) collectLocalObjects(ctx context.Context, report *SyncReport, optIn kindOptIn) (map[string]localObject, error) {
	objects := map[string]localObject{}
	groups, err := e.store.GroupList(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		payload, err := marshalObject(groupObject{
			ID: group.ID, ParentID: group.ParentID, Name: group.Name, Sort: group.Sort,
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		objects[group.ID] = localObject{KindGroup, payload}
	}
	credentials, err := e.store.CredentialList(ctx)
	if err != nil {
		return nil, err
	}
	if len(credentials) > 0 {
		if err := e.requireVault(ctx); err != nil {
			return nil, err
		}
	}
	for _, row := range credentials {
		secret, err := e.vault.DecryptCredentialString(ctx, row)
		if err != nil {
			report.warnf("凭据 %s 解密失败, 本次不同步: %v", row.ID, err)
			continue
		}
		payload, err := marshalObject(credentialObject{
			ID: row.ID, Name: row.Name, Kind: row.Kind, Secret: secret, UpdatedAt: row.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		objects[row.ID] = localObject{KindCredential, payload}
	}
	snippets, err := e.store.SnippetList(ctx)
	if err != nil {
		return nil, err
	}
	for _, snippet := range snippets {
		payload, err := marshalObject(snippetObject{
			ID: snippet.ID, GroupID: snippet.GroupID, Name: snippet.Name, Body: snippet.Body,
			Sort: snippet.Sort, CreatedAt: snippet.CreatedAt, UpdatedAt: snippet.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		objects[snippet.ID] = localObject{KindSnippet, payload}
	}
	assets, err := e.store.AssetList(ctx, true)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		objects[asset.ID] = localObject{KindAsset, payload}
	}
	tombstones, err := e.syncTombstoneList(ctx)
	if err != nil {
		return nil, err
	}
	knownHostMetas, err := e.knownHostTombstoneMetas(ctx)
	if err != nil {
		return nil, err
	}
	for _, tombstone := range tombstones {
		if tombstone.Kind == KindKnownHost && !optIn.knownHost {
			continue
		}
		if tombstone.Kind == KindAIProfile && !optIn.aiProfile {
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
			return nil, err
		}
		objects[tombstone.ID] = localObject{KindTombstone, payload}
	}
	credentialTombstones, err := e.store.CredentialTombstoneList(ctx)
	if err != nil {
		return nil, err
	}
	for _, tombstone := range credentialTombstones {
		payload, err := marshalObject(tombstoneObject{TargetKind: KindCredential, DeletedAt: tombstone.DeletedAt})
		if err != nil {
			return nil, err
		}
		objects[tombstone.ID] = localObject{KindTombstone, payload}
	}
	transcripts, err := e.store.TranscriptListOptedIn(ctx)
	if err != nil {
		return nil, err
	}
	for _, transcript := range transcripts {
		payload, err := e.transcriptPayload(ctx, transcript)
		if err != nil {
			report.warnf("会话记录 %s 打包失败, 本次不同步: %v", transcript.ID, err)
			continue
		}
		objects[transcript.ID] = localObject{KindTranscript, payload}
	}
	if optIn.knownHost {
		knownHosts, err := e.store.KnownHostList(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range knownHosts {
			payload, err := marshalObject(knownHostObject{
				ID: row.ID, Host: row.Host, Port: row.Port, KeyType: row.KeyType,
				Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
			})
			if err != nil {
				return nil, err
			}
			objects[row.ID] = localObject{KindKnownHost, payload}
		}
	}
	if optIn.aiProfile {
		profileState, profileRevision, _, err := e.aiProfilesLoad(ctx)
		if err != nil {
			if !errors.Is(err, errAIProfilesCorrupt) {
				return nil, err
			}
			report.warnf("AI 模型档案数据损坏, 本次不同步: %v", err)
		} else {
			for _, record := range profileState.Profiles {
				key, err := e.revealAIProfileKey(ctx, record.APIKey)
				if err != nil {
					report.warnf("AI 模型档案 %s 解密失败, 本次不同步: %v", record.ID, err)
					continue
				}
				record.APIKey = key
				payload, err := marshalObject(aiProfilePayloadFromRecord(record, profileRevision))
				if err != nil {
					return nil, err
				}
				objects[record.ID] = localObject{KindAIProfile, payload}
			}
		}
	}
	return objects, nil
}

func (e *Engine) transcriptPayload(ctx context.Context, transcript store.TranscriptRow) ([]byte, error) {
	object := transcriptObject{
		ID: transcript.ID, SessionID: transcript.SessionID, AssetID: transcript.AssetID,
		AssetName: transcript.AssetName, AssetKind: transcript.AssetKind,
		StartedAt: transcript.StartedAt, Truncated: transcript.Truncated,
	}
	if transcript.EndedAt != nil {
		object.EndedAt = *transcript.EndedAt
	}
	if transcript.Bytes > syncTranscriptMaxContentBytes {
		object.ContentOmitted = true
		object.Bytes = transcript.Bytes
		object.Chunks = transcript.Chunks
		return marshalObject(object)
	}
	chunks, err := e.transcriptChunksAll(ctx, transcript.ID)
	if err != nil {
		return nil, err
	}
	object.Content = make([]transcriptChunkObject, 0, len(chunks))
	for _, chunk := range chunks {
		object.Content = append(object.Content, transcriptChunkObject{
			Seq: chunk.Seq, TabID: chunk.TabID, TS: chunk.TS, Kind: chunk.Kind, Data: chunk.Data,
		})
	}
	object.Bytes = transcript.Bytes
	object.Chunks = transcript.Chunks
	return marshalObject(object)
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

// applyRemoteObject 解密并 LWW 应用一个远端对象; 应用失败隔离为警告, 不阻断整轮同步。
// identical 表示远端与本机内容一致, 视为已对账; 本地胜出时 payload_hash 置空, 由 blob 哈希差异驱动回推。
// opt-in 硬关闭的 known_host/AI 档案(含其墓碑)只计数跳过: 不应用也不记对账, 保持未对账供日后开启再拉取。
func (e *Engine) applyRemoteObject(ctx context.Context, userID string, object WireObjectSeq, dek []byte, report *SyncReport, optIn kindOptIn) {
	report.Pulled++
	kind, plaintext, err := tryOpenObject(dek, object.Blob, object.ID)
	if err != nil {
		report.DecryptFailed++
		report.warnf("对象 %s 解密失败(密钥不匹配或数据损坏), 已隔离", object.ID)
		e.quarantineObject(ctx, userID, object.ID, hashBlob(object.Blob))
		return
	}
	if !optIn.allowsRemote(kind, plaintext) {
		report.PullSkipped++
		return
	}
	payloadHash := objectPayloadHash(plaintext)
	applied, identical := e.applyDecryptedObject(ctx, object.ID, kind, plaintext, report)
	report.PullSkipped++
	if applied {
		report.Applied++
		report.PullSkipped--
	}
	if applied || identical {
		e.syncStatePut(ctx, userID, object.ID, &payloadHash, hashBlob(object.Blob))
	} else {
		e.syncStatePut(ctx, userID, object.ID, nil, hashBlob(object.Blob))
	}
}

// quarantineObject 记录不可解密对象的 blob 哈希, 避免每轮重复拉取; 本地有对应内容时推送会自然覆盖。
func (e *Engine) quarantineObject(ctx context.Context, userID, objectID string, blobHash [32]byte) {
	if err := e.syncStatePut(ctx, userID, objectID, nil, blobHash); err != nil {
		e.logger.Warn("sync quarantine record failed", "error", err)
	}
}

func (e *Engine) applyGroupObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload groupObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("分组对象载荷损坏: %v", err)
		return false, false
	}
	local, err := e.store.GroupGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查分组 %s: %v", payload.ID, err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查分组 %s 的删除墓碑: %v", payload.ID, err)
		return false, false
	} else if found && tombstone.DeletedAt >= payload.UpdatedAt {
		return false, false
	}
	if exists {
		localPayload, err := marshalObject(groupObject{
			ID: local.ID, ParentID: local.ParentID, Name: local.Name, Sort: local.Sort,
			CreatedAt: local.CreatedAt, UpdatedAt: local.UpdatedAt,
		})
		if err != nil {
			report.warnf("分组 %s 本地载荷编码失败: %v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.UpdatedAt, local.UpdatedAt, plaintext, localPayload) {
			return false, false
		}
	}
	parentID := payload.ParentID
	if parentID != nil {
		if *parentID == "" {
			parentID = nil
		} else if _, err := e.store.GroupGet(ctx, *parentID); isNotFound(err) {
			report.warnf("分组 %s 的父级 %s 不存在, 已按顶级分组导入", payload.ID, *parentID)
			parentID = nil
		} else if err != nil {
			report.warnf("无法检查分组 %s 的父级: %v", payload.ID, err)
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
			report.warnf("分组 %s 的父级会在合并后形成循环, 已按顶级分组导入", payload.ID)
			parentID = nil
		case groupTopologyTooDeep:
			report.warnf("分组 %s 合并后的祖先链超过 64 层, 已按顶级分组导入", payload.ID)
			parentID = nil
		}
	}
	if err := e.groupUpsert(ctx, payload, parentID); err != nil {
		report.warnf("分组 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

// groupUpsert 与 store.GroupUpsert 同款语义, 并在同一事务清除同 ID 的删除墓碑:
// 较新分组胜过旧墓碑后不得留下可回滚对象的墓碑。
func (e *Engine) groupUpsert(ctx context.Context, payload groupObject, parentID *string) error {
	if err := store.EnsureID(payload.ID); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Name) == "" {
		return ipc.NewError(ipc.CodeBadParam, "分组名称不能为空")
	}
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := e.groupUpsertTx(ctx, tx, payload, parentID); err != nil {
		return err
	}
	return tx.Commit()
}

// groupUpsertTx 是 groupUpsert 的事务内版本, 供 ImportBundle 把一个对象类的多次写入并入同一事务。
func (e *Engine) groupUpsertTx(ctx context.Context, tx *sql.Tx, payload groupObject, parentID *string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO asset_group(id, parent_id, name, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET parent_id=excluded.parent_id, name=excluded.name, sort=excluded.sort, updated_at=excluded.updated_at`,
		payload.ID, parentID, strings.TrimSpace(payload.Name), payload.Sort, payload.CreatedAt, payload.UpdatedAt); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sync_tombstone WHERE id = ?", payload.ID); err != nil {
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
		report.warnf("资产对象载荷损坏: %v", err)
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
			report.warnf("资产 %s 本地载荷编码失败: %v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(objectRevision(payload.UpdatedAt, payload.DeletedAt), objectRevision(local.UpdatedAt, local.DeletedAt), plaintext, localPayload) {
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
			report.warnf("资产 %s 引用的分组 %s 不存在, 已清除该引用", payload.ID, *row.GroupID)
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
			report.warnf("资产 %s 引用的凭据 %s 不存在, 已清除该引用", payload.ID, *row.CredID)
			row.CredID = nil
		} else if err != nil {
			report.warnf("无法检查资产 %s 的凭据: %v", payload.ID, err)
			return false, false
		}
	}
	if _, err := e.store.AssetUpsert(ctx, row); err != nil {
		report.warnf("资产 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyCredentialObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload credentialObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("凭据对象载荷损坏: %v", err)
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
		report.warnf("无法检查凭据 %s 的删除墓碑: %v", payload.ID, err)
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
			report.warnf("凭据 %s 本地载荷编码失败: %v", payload.ID, err)
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
	if err := e.credentialUpsert(ctx, payload, nonce, blob); err != nil {
		report.warnf("凭据 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

// credentialUpsert 与 store.CredentialPut 同款, 但保留对端修订号: store 层按本机时钟写 updated_at,
// 同步载荷哈希以修订号为准, 必须原样保留才能幂等对账。
// 写入与删除墓碑清除在同一事务: 较新凭据胜过旧墓碑后不得留下可回滚对象的墓碑。
func (e *Engine) credentialUpsert(ctx context.Context, payload credentialObject, nonce, blob []byte) error {
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := e.credentialUpsertTx(ctx, tx, payload, nonce, blob); err != nil {
		return err
	}
	return tx.Commit()
}

// credentialUpsertTx 是 credentialUpsert 的事务内版本, 供 ImportBundle 把一个对象类的多次写入并入同一事务。
func (e *Engine) credentialUpsertTx(ctx context.Context, tx *sql.Tx, payload credentialObject, nonce, blob []byte) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO credential(id, name, kind, cipher, nonce, blob, kek_hint, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, cipher=excluded.cipher,
nonce=excluded.nonce, blob=excluded.blob, kek_hint=excluded.kek_hint, updated_at=excluded.updated_at`,
		payload.ID, payload.Name, payload.Kind, store.CipherAES256GCM, nonce, blob, e.vault.KEKHint(),
		payload.UpdatedAt, payload.UpdatedAt); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM credential_tombstone WHERE id = ?", payload.ID); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func (e *Engine) applySnippetObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload snippetObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("片段对象载荷损坏: %v", err)
		return false, false
	}
	local, err := e.store.SnippetGet(ctx, payload.ID)
	exists := err == nil
	if err != nil && !isNotFound(err) {
		report.warnf("无法检查片段 %s: %v", payload.ID, err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查片段 %s 的删除墓碑: %v", payload.ID, err)
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
			report.warnf("片段 %s 本地载荷编码失败: %v", payload.ID, err)
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
			report.warnf("片段 %s 引用的分组 %s 不存在, 已清除该引用", payload.ID, *row.GroupID)
			row.GroupID = nil
		} else if err != nil {
			report.warnf("无法检查片段 %s 的分组: %v", payload.ID, err)
			return false, false
		}
	}
	if err := e.snippetUpsert(ctx, row); err != nil {
		report.warnf("片段 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyTombstoneObject(ctx context.Context, objectID string, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload tombstoneObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("墓碑对象载荷损坏: %v", err)
		return false, false
	}
	switch payload.TargetKind {
	case KindGroup:
		return e.applyGroupTombstone(ctx, objectID, payload.DeletedAt, report)
	case KindSnippet:
		return e.applySnippetTombstone(ctx, objectID, payload.DeletedAt, report)
	case KindCredential:
		return e.applyCredentialTombstone(ctx, objectID, payload.DeletedAt, report)
	case KindTranscript:
		return e.applyTranscriptTombstone(ctx, objectID, payload.DeletedAt, report)
	case KindKnownHost:
		return e.applyKnownHostTombstone(ctx, objectID, payload, report)
	case KindAIProfile:
		return e.applyAIProfileTombstone(ctx, objectID, payload.DeletedAt, report)
	default:
		report.warnf("墓碑对象目标种类 %s 不受支持", payload.TargetKind)
		return false, false
	}
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
	if err := e.syncTombstonePut(ctx, objectID, KindGroup, deletedAt); err != nil {
		report.warnf("分组 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	if exists {
		if err := e.store.GroupDelete(ctx, objectID); err != nil {
			report.warnf("分组 %s 按墓碑删除失败: %v", objectID, err)
			return false, false
		}
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
		report.warnf("片段 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	if exists {
		if err := e.store.SnippetDelete(ctx, objectID); err != nil {
			report.warnf("片段 %s 按墓碑删除失败: %v", objectID, err)
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
			report.warnf("凭据 %s 按墓碑删除失败: %v", objectID, err)
			return false, false
		}
	}
	if err := e.store.CredentialTombstonePut(ctx, objectID, deletedAt); err != nil {
		report.warnf("凭据 %s 的删除墓碑记录失败: %v", objectID, err)
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
		// 本机 opt-out 记录保留: 视为已与远端墓碑对账, 本地副本不参与推送。
		return false, true
	}
	if err := e.syncTombstonePut(ctx, objectID, KindTranscript, deletedAt); err != nil {
		report.warnf("会话记录 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	if exists {
		if _, err := e.store.DB().ExecContext(ctx, "DELETE FROM transcript WHERE id=?", objectID); err != nil {
			report.warnf("会话记录 %s 按墓碑删除失败: %v", objectID, err)
			return false, false
		}
	}
	return true, false
}

func (e *Engine) applyTranscriptObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload transcriptObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("会话记录对象载荷损坏: %v", err)
		return false, false
	}
	if payload.EndedAt <= 0 {
		report.warnf("会话记录 %s 未结束, 不同步", payload.ID)
		return false, false
	}
	if _, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查会话记录 %s 的删除墓碑: %v", payload.ID, err)
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
		// 会话记录内容不可变: 本地已有即视为一致。
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
		report.warnf("已知主机对象载荷损坏: %v", err)
		return false, false
	}
	if tombstone, found, err := e.syncTombstoneGet(ctx, payload.ID); err != nil {
		report.warnf("无法检查已知主机 %s 的删除墓碑: %v", payload.ID, err)
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
			report.warnf("已知主机 %s 本地载荷编码失败: %v", payload.ID, err)
			return false, false
		}
		if bytes.Equal(plaintext, localPayload) {
			return false, true
		}
		if !remoteWins(payload.AddedAt, local.AddedAt, plaintext, localPayload) {
			return false, false
		}
	}
	// 同一 (host, port, keyType) 只能有一行: 不同 ID 的同行记录按 LWW 决胜,
	// 败者行删除并以胜者修订号立碑, 保证多设备对同一主机收敛到同一对象。
	displacedID := ""
	if conflict, found, err := e.store.KnownHostGet(ctx, payload.Host, payload.Port, payload.KeyType); err != nil {
		report.warnf("无法检查已知主机 %s 的三元组冲突: %v", payload.ID, err)
		return false, false
	} else if found && conflict.ID != payload.ID {
		conflictPayload, err := marshalObject(knownHostObject{
			ID: conflict.ID, Host: conflict.Host, Port: conflict.Port, KeyType: conflict.KeyType,
			Fingerprint: conflict.Fingerprint, AddedAt: conflict.AddedAt,
		})
		if err != nil {
			report.warnf("已知主机 %s 冲突载荷编码失败: %v", payload.ID, err)
			return false, false
		}
		if !remoteWins(payload.AddedAt, conflict.AddedAt, plaintext, conflictPayload) {
			// 本地胜者保住三元组, 但对端败者对象仍留在服务端: 按胜者修订号为败者立碑,
			// 本轮 collect/push 即以墓碑覆盖之, 否则胜者日后被删除时旧指纹会在新设备复活。
			// 同 ID 的本地存活行(不同三元组的化身)与败者墓碑不能共存, 先让出原 ID 再立碑。
			var incarnation *store.KnownHostRow
			if exists {
				incarnation = &local
			}
			if err := e.knownHostTombstoneLoser(ctx, payload, incarnation, conflict.AddedAt); err != nil {
				report.warnf("已知主机败者 %s 的删除墓碑记录失败: %v", payload.ID, err)
				return false, false
			}
			return false, false
		}
		displacedID = conflict.ID
	}
	if err := e.knownHostUpsert(ctx, payload, displacedID); err != nil {
		report.warnf("已知主机 %s 应用失败: %v", payload.ID, err)
		return false, false
	}
	return true, false
}

// applyKnownHostTombstone 区分冲突墓碑与用户主动删除: 冲突墓碑携带原败者三元组,
// 本地同 ID 不同三元组且修订号不新的行是胜出的化身, 不得按用户删除清掉——安全 re-ID
// 并随本轮 collect/push 传播, 冲突墓碑只清原槽位。同三元组败者行与用户删除按 ID 清除。
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
			report.warnf("已知主机化身 %s 的 re-ID 失败: %v", objectID, err)
			return false, false
		}
		tx, err := e.store.DB().BeginTx(ctx, nil)
		if err != nil {
			report.warnf("已知主机化身 %s 的 re-ID 失败: %v", objectID, err)
			return false, false
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, "UPDATE known_host SET id = ? WHERE id = ?", newID, local.ID); err != nil {
			report.warnf("已知主机化身 %s 的 re-ID 失败: %v", objectID, err)
			return false, false
		}
		meta := &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType}
		if err := e.knownHostTombstoneRecordTx(ctx, tx, objectID, payload.DeletedAt, meta); err != nil {
			report.warnf("已知主机 %s 的冲突墓碑记录失败: %v", objectID, err)
			return false, false
		}
		if err := tx.Commit(); err != nil {
			report.warnf("已知主机 %s 的冲突墓碑记录失败: %v", objectID, err)
			return false, false
		}
		return true, false
	}
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		report.warnf("已知主机 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	defer func() { _ = tx.Rollback() }()
	if exists {
		if _, err := tx.ExecContext(ctx, "DELETE FROM known_host WHERE id = ?", objectID); err != nil {
			report.warnf("已知主机 %s 按墓碑删除失败: %v", objectID, err)
			return false, false
		}
	}
	var meta *knownHostTombstoneMeta
	if conflict {
		meta = &knownHostTombstoneMeta{Host: payload.Host, Port: payload.Port, KeyType: payload.KeyType}
	}
	if err := e.knownHostTombstoneRecordTx(ctx, tx, objectID, payload.DeletedAt, meta); err != nil {
		report.warnf("已知主机 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	if err := tx.Commit(); err != nil {
		report.warnf("已知主机 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	return true, false
}

func (e *Engine) applyAIProfileObject(ctx context.Context, plaintext []byte, report *SyncReport) (bool, bool) {
	var payload aiProfileObject
	if err := unmarshalObject(plaintext, &payload); err != nil {
		report.warnf("AI 模型档案对象载荷损坏: %v", err)
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
		report.warnf("无法检查 AI 模型档案 %s 的删除墓碑: %v", payload.ID, err)
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
			report.warnf("AI 模型档案 %s 本地载荷编码失败: %v", payload.ID, err)
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
	// 档案共享一个 LWW 时钟: 修订号只增不减, 本地较新编辑不得被较旧对端对象回滚。
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
		report.warnf("AI 模型档案 %s 的删除墓碑记录失败: %v", objectID, err)
		return false, false
	}
	if exists {
		state.remove(objectID)
		state.ensureActive()
		if err := e.aiProfilesSave(ctx, state, revision, ""); err != nil {
			report.warnf("AI 模型档案 %s 按墓碑删除失败: %v", objectID, err)
			return false, false
		}
	}
	return true, false
}

func unmarshalObject(plaintext []byte, dst any) error {
	if err := json.Unmarshal(plaintext, dst); err != nil {
		return ipc.WrapError(ipc.CodeBadParam, "同步对象载荷不是合法 JSON", err)
	}
	return nil
}

func (e *Engine) requireVault(ctx context.Context) error {
	if e.vault == nil {
		return ipc.NewError(ipc.CodeVaultLocked, "凭据库不可用, 无法同步凭据")
	}
	status := e.vault.Status()
	if !status.Initialized {
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库未初始化, 无法同步凭据")
	}
	if !status.Unlocked {
		return ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定, 请先解锁")
	}
	return nil
}

// objectKindRank 保证同一批推送内被依赖对象先于依赖者: 拉取端按 seq 顺序应用, 引用必须在应用前已存在。
// 墓碑必须排在全部内容种类之后: 同批内容与其墓碑冲突时, 服务端以墓碑覆盖, 拉取端才对账收敛。
func objectKindRank(kind string) int {
	switch kind {
	case KindGroup:
		return 0
	case KindCredential:
		return 1
	case KindSnippet:
		return 2
	case KindAsset:
		return 3
	case KindKnownHost:
		return 4
	case KindAIProfile:
		return 5
	case KindTombstone:
		return 6
	case KindTranscript:
		return 7
	default:
		return 8
	}
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
	return ids
}
