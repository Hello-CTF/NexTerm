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
	maxPushBatchBytes = 32 << 20
	maxSyncAttempts   = 3
	maxPullPages      = 10000
)

// maxPullBytes 是客户端单页拉取预算; 测试可临时调小以构造分页场景。
var maxPullBytes int64 = maxPullWireBytes

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
	completed, err := e.pullAllAndApply(ctx, session, &cursor, unreconciled, report)
	if err != nil {
		return false, err
	}
	if !completed {
		// 对账未完成(stall): 持久化进度但本轮不得 collect/push, 不得采用最新 head 继续。
		if err := e.saveCursor(ctx, userID, cursor); err != nil {
			return false, err
		}
		return false, ipc.NewError(ipc.CodeDisconnected, "同步对账未完成(部分对象未返回), 请稍后重试")
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
	// 收尾无补拉 ID, 空页即自然完成; 若超出有界页数仍未完成, 记警告但不影响已完成的推送。
	if completed, err := e.pullAllAndApply(ctx, session, &cursor, nil, report); err != nil {
		return false, err
	} else if !completed {
		report.warnf("收尾拉取未能在有界页数内完成, 剩余对象留待下一轮")
	}
	if err := e.saveCursor(ctx, userID, cursor); err != nil {
		return false, err
	}
	report.Head = cursor.Head
	report.Seq = cursor.Seq
	return false, nil
}

// pullAllAndApply 耗尽所有游标分页, 并确认全部 unreconciled ID 已返回或已不存在,
// 然后才允许进入推送。游标进度只由 next_seq 推进(补拉 ID 的 seq 不得推进游标或伪造完成);
// 首个对象必完整返回的预算规则保证每页至少消化一个对象, 循环必然收敛。
// 返回 completed=false 表示 stall(对账未完成), 调用方不得在本轮 collect/push。
func (e *Engine) pullAllAndApply(ctx context.Context, session *remoteSession, cursor *syncCursor, unreconciled []string, report *SyncReport) (bool, error) {
	remaining := make(map[string]bool, len(unreconciled))
	for _, id := range unreconciled {
		remaining[id] = true
	}
	stalls := 0
	for page := 0; page < maxPullPages; page++ {
		lookup := make([]string, 0, len(remaining))
		for id := range remaining {
			lookup = append(lookup, id)
		}
		if len(lookup) > maxPullIDLookup {
			lookup = lookup[:maxPullIDLookup]
		}
		pullResponse, err := session.client.pull(ctx, cursor.Seq, lookup, maxPullBytes)
		if err != nil {
			return false, err
		}
		returned := 0
		for _, object := range pullResponse.Objects {
			e.applyRemoteObject(ctx, session.userID, object, session.dek, report)
			delete(remaining, object.ID)
			returned++
		}
		// 游标只由 next_seq(游标对象)推进; 补拉 ID 不影响游标位置与完成判定。
		cursor.Seq = pullResponse.NextSeq
		cursor.Head = pullResponse.Head
		if pullResponse.CursorDone && len(remaining) == 0 {
			return true, nil
		}
		if returned == 0 && len(remaining) > 0 && pullResponse.CursorDone {
			stalls++
			if stalls >= 3 {
				e.logger.Warn("sync pull stalled on missing objects; deferring to next round", "remaining", len(remaining))
				return false, nil
			}
		} else {
			stalls = 0
		}
	}
	e.logger.Warn("sync pull exceeded page bound; deferring to next round", "remaining", len(remaining))
	return false, nil
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
ON CONFLICT(id) DO UPDATE SET deleted_at = `+scalarMax(e.store.Backend())+`(sync_tombstone.deleted_at, excluded.deleted_at)`, objectID, kind, deletedAt)
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
