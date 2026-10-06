package sync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

const (
	maxPullBytes      = maxPullWireBytes
	maxPushBatchBytes = 32 << 20
	maxSyncAttempts   = 3
)

type SyncReport struct {
	Pulled        int      `json:"pulled"`
	Applied       int      `json:"applied"`
	PullSkipped   int      `json:"pullSkipped"`
	DecryptFailed int      `json:"decryptFailed"`
	Pushed        int      `json:"pushed"`
	Conflicts     int      `json:"conflicts"`
	Head          string   `json:"head"`
	Seq           int64    `json:"seq"`
	Warnings      []string `json:"warnings,omitempty"`
}

type remoteSession struct {
	key    string
	userID string
	client *remoteClient
	dek    []byte
}

// Engine 是 v2 全量载荷对象协议的客户端引擎: 同一本地副本在未登录与登录后保持一致,
// 登录仅挂接同步。引擎自身无状态, 会话与 DEK 仅内存缓存。
type Engine struct {
	store  *store.Store
	vault  *vault.Vault
	logger *slog.Logger

	sessionMu sync.Mutex
	session   *remoteSession
}

func NewEngine(database *store.Store, credentialVault *vault.Vault, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: database, vault: credentialVault, logger: logger}
}

// Sync 执行一轮完整同步: 对账 → 拉取合并 → 推送本地变更。冲突(其他设备先推或回滚)自动重试。
func (e *Engine) Sync(ctx context.Context, config RemoteConfig) (SyncReport, error) {
	report := SyncReport{}
	for attempt := 0; attempt < maxSyncAttempts; attempt++ {
		session, err := e.ensureSession(ctx, config)
		if err != nil {
			return report, err
		}
		conflict, err := e.syncOnce(ctx, session, &report)
		if errors.Is(err, errSessionExpired) {
			e.dropSession(session.key)
			continue
		}
		if err != nil {
			return report, err
		}
		if !conflict {
			return report, nil
		}
		report.Conflicts++
	}
	return report, ipc.NewError(ipc.CodeForbidden, "同步冲突重试次数过多, 请稍后重试")
}

func (e *Engine) ensureSession(ctx context.Context, config RemoteConfig) (*remoteSession, error) {
	key := config.URL + "\x00" + config.Username
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if e.session != nil && e.session.key == key {
		return e.session, nil
	}
	client, err := newRemoteClient(config)
	if err != nil {
		return nil, err
	}
	if err := client.login(ctx, config.Username, config.Password); err != nil {
		return nil, err
	}
	userID, err := client.me(ctx)
	if err != nil {
		return nil, err
	}
	envelopes, err := client.dekEnvelopes(ctx)
	if err != nil {
		return nil, err
	}
	dek, err := vault.UnwrapUserDEK(config.Password, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		return nil, err
	}
	e.session = &remoteSession{key: key, userID: userID, client: client, dek: dek}
	return e.session, nil
}

func (e *Engine) dropSession(key string) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if e.session != nil && e.session.key == key {
		clear(e.session.dek)
		e.session = nil
	}
}

// syncOnce 单轮同步; conflict 为 true 表示推送时 head 不一致, 需要重新对账后再试。
func (e *Engine) syncOnce(ctx context.Context, session *remoteSession, report *SyncReport) (bool, error) {
	userID := session.userID
	idsResponse, err := session.client.ids(ctx)
	if err != nil {
		return false, err
	}
	server := make(map[string]IDEntry, len(idsResponse.Entries))
	for _, entry := range idsResponse.Entries {
		server[entry.ID] = entry
	}
	cursor, err := e.loadCursor(ctx, userID)
	if err != nil {
		return false, err
	}
	unreconciled := make([]string, 0, len(server))
	for _, entry := range idsResponse.Entries {
		_, blobHash, found, err := e.syncStateGet(ctx, userID, entry.ID)
		if err != nil {
			return false, err
		}
		if !found || blobHash != entry.BlobHash {
			unreconciled = append(unreconciled, entry.ID)
		}
	}
	if len(unreconciled) > maxPullIDLookup {
		unreconciled = unreconciled[:maxPullIDLookup]
	}
	if err := e.pullAndApply(ctx, session, &cursor, unreconciled, report); err != nil {
		return false, err
	}
	objects, err := e.collectLocalObjects(ctx, report)
	if err != nil {
		return false, err
	}
	pending := make([]WireObject, 0, len(objects))
	pendingHashes := make([]string, 0, len(objects))
	for _, id := range sortObjectsForPush(objects) {
		local := objects[id]
		payloadHash := objectPayloadHash(local.plaintext)
		statePayloadHash, stateBlobHash, found, err := e.syncStateGet(ctx, userID, id)
		if err != nil {
			return false, err
		}
		serverEntry, onServer := server[id]
		if found && statePayloadHash == payloadHash && onServer && stateBlobHash == serverEntry.BlobHash {
			continue
		}
		blob, err := sealObject(session.dek, local.plaintext, id, local.kind)
		if err != nil {
			return false, err
		}
		pending = append(pending, WireObject{ID: id, Blob: blob})
		pendingHashes = append(pendingHashes, payloadHash)
	}
	for len(pending) > 0 {
		batch, batchHashes, rest, restHashes := takePushBatch(pending, pendingHashes)
		response, err := session.client.push(ctx, cursor.Head, batch)
		if errors.Is(err, errHeadMismatch) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		for i, object := range batch {
			payloadHash := batchHashes[i]
			if err := e.syncStatePut(ctx, userID, object.ID, &payloadHash, hashBlob(object.Blob)); err != nil {
				return false, err
			}
		}
		report.Pushed += len(batch)
		cursor.Head = response.Head
		pending, pendingHashes = rest, restHashes
	}
	// 收尾拉取: 把游标推进到推送时点, 使空闲同步保持静默, 并立即看到推送期间其他设备的变更。
	if err := e.pullAndApply(ctx, session, &cursor, nil, report); err != nil {
		return false, err
	}
	if err := e.saveCursor(ctx, userID, cursor); err != nil {
		return false, err
	}
	report.Head = cursor.Head
	report.Seq = cursor.Seq
	return false, nil
}

func (e *Engine) pullAndApply(ctx context.Context, session *remoteSession, cursor *syncCursor, ids []string, report *SyncReport) error {
	pullResponse, err := session.client.pull(ctx, cursor.Seq, ids, maxPullBytes)
	if err != nil {
		return err
	}
	for _, object := range pullResponse.Objects {
		e.applyRemoteObject(ctx, session.userID, object, session.dek, report)
		if object.Seq > cursor.Seq {
			cursor.Seq = object.Seq
		}
	}
	cursor.Head = pullResponse.Head
	return nil
}

func takePushBatch(objects []WireObject, hashes []string) (batch []WireObject, batchHashes []string, rest []WireObject, restHashes []string) {
	batch = []WireObject{}
	batchHashes = []string{}
	size := 0
	for i, object := range objects {
		if i > 0 && int64(size+len(object.Blob)) > maxPushBatchBytes {
			return batch, batchHashes, objects[i:], hashes[i:]
		}
		batch = append(batch, object)
		batchHashes = append(batchHashes, hashes[i])
		size += len(object.Blob)
	}
	return batch, batchHashes, nil, nil
}

type syncCursor struct {
	Seq  int64  `json:"seq"`
	Head string `json:"head"`
}

func cursorSettingKey(userID string) string {
	return "sync.cursor." + userID
}

func (e *Engine) loadCursor(ctx context.Context, userID string) (syncCursor, error) {
	cursor := syncCursor{}
	raw, found, err := e.store.SettingGet(ctx, cursorSettingKey(userID))
	if err != nil {
		return cursor, err
	}
	if !found || raw == "" {
		return cursor, nil
	}
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil {
		e.logger.Warn("sync cursor corrupted; resetting", "error", err)
		return syncCursor{}, nil
	}
	return cursor, nil
}

func (e *Engine) saveCursor(ctx context.Context, userID string, cursor syncCursor) error {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码同步游标", err)
	}
	return e.store.SettingSet(ctx, cursorSettingKey(userID), string(encoded))
}

func (e *Engine) syncStateGet(ctx context.Context, userID, objectID string) (payloadHash string, blobHash string, found bool, returnErr error) {
	var payload []byte
	var blob []byte
	err := e.store.DB().QueryRowContext(ctx,
		"SELECT payload_hash, blob_hash FROM sync_state WHERE user_id = ? AND object_id = ?",
		userID, objectID).Scan(&payload, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if payload != nil {
		payloadHash = string(payload)
	}
	return payloadHash, hex.EncodeToString(blob), true, nil
}

func (e *Engine) syncStatePut(ctx context.Context, userID, objectID string, payloadHash *string, blobHash [32]byte) error {
	var payload []byte
	if payloadHash != nil {
		payload = []byte(*payloadHash)
	}
	_, err := e.store.DB().ExecContext(ctx, `INSERT INTO sync_state(user_id, object_id, payload_hash, blob_hash) VALUES(?,?,?,?)
ON CONFLICT(user_id, object_id) DO UPDATE SET payload_hash = excluded.payload_hash, blob_hash = excluded.blob_hash`,
		userID, objectID, payload, blobHash[:])
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

type syncTombstoneRow struct {
	ID        string
	Kind      string
	DeletedAt int64
}

func (e *Engine) syncTombstoneList(ctx context.Context) ([]syncTombstoneRow, error) {
	rows, err := e.store.DB().QueryContext(ctx, "SELECT id, kind, deleted_at FROM sync_tombstone ORDER BY id")
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer rows.Close()
	result := []syncTombstoneRow{}
	for rows.Next() {
		var row syncTombstoneRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.DeletedAt); err != nil {
			return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (e *Engine) syncTombstoneGet(ctx context.Context, objectID string) (syncTombstoneRow, bool, error) {
	var row syncTombstoneRow
	err := e.store.DB().QueryRowContext(ctx,
		"SELECT id, kind, deleted_at FROM sync_tombstone WHERE id = ?", objectID).
		Scan(&row.ID, &row.Kind, &row.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return syncTombstoneRow{}, false, nil
	}
	if err != nil {
		return syncTombstoneRow{}, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return row, true, nil
}

func (e *Engine) syncTombstonePut(ctx context.Context, objectID, kind string, deletedAt int64) error {
	_, err := e.store.DB().ExecContext(ctx, `INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at = max(deleted_at, excluded.deleted_at)`, objectID, kind, deletedAt)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

// snippetUpsert 与 store.GroupUpsert 同款语义: 按 ID 幂等写入并保留对端修订号,
// 并在同一事务清除同 ID 的删除墓碑: 较新片段胜过旧墓碑后不得留下可回滚对象的墓碑。
func (e *Engine) snippetUpsert(ctx context.Context, row store.SnippetRow) error {
	tx, err := e.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET group_id=excluded.group_id, name=excluded.name, body=excluded.body,
sort=excluded.sort, updated_at=excluded.updated_at`,
		row.ID, row.GroupID, row.Name, row.Body, row.Sort, row.CreatedAt, row.UpdatedAt); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sync_tombstone WHERE id = ?", row.ID); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return tx.Commit()
}

func isNotFound(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound
}
