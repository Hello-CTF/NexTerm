package sync

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

	opMu      sync.Mutex
	sessionMu sync.Mutex
	session   *remoteSession
	pending   map[string]*pendingLinkLogin
}

func NewEngine(database *store.Store, credentialVault *vault.Vault, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: database, vault: credentialVault, logger: logger, pending: map[string]*pendingLinkLogin{}}
}

// kindOptIn 是同步对象在一轮同步内的 opt-in 快照; 零值即硬关闭。
type kindOptIn struct {
	knownHost    bool
	aiProfile    bool
	aiPermission bool
	preferences  bool
}

// loadKindOptIn 按会话用户读取同步对象的 opt-in; 无会话身份(userID 为空)时硬关闭, 不退化为全局设置。
func (e *Engine) loadKindOptIn(ctx context.Context, userID string) (kindOptIn, error) {
	if userID == "" {
		return kindOptIn{}, nil
	}
	knownHost, err := e.store.KnownHostSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	aiProfile, err := e.store.AIProfileSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	aiPermission, err := e.store.AIPermissionSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	preferences, err := e.store.PreferencesSyncOptIn(ctx, userID)
	if err != nil {
		return kindOptIn{}, err
	}
	return kindOptIn{knownHost: knownHost, aiProfile: aiProfile, aiPermission: aiPermission, preferences: preferences}, nil
}

// allowsRemote 判定远端对象(含墓碑)本轮是否可应用; 硬关闭的种类不应用也不记对账,
// 保持未对账状态, 日后开启 opt-in 的同步轮即可再拉取合并。
func (o kindOptIn) allowsRemote(kind string, plaintext []byte) bool {
	switch kind {
	case KindKnownHost:
		return o.knownHost
	case KindAIProfile:
		return o.aiProfile
	case KindAIPermission:
		return o.aiPermission
	case KindPreference:
		return o.preferences
	case KindTombstone:
		var payload tombstoneObject
		if err := json.Unmarshal(plaintext, &payload); err != nil {
			return true
		}
		switch payload.TargetKind {
		case KindKnownHost:
			return o.knownHost
		case KindAIProfile:
			return o.aiProfile
		case KindAIPermission:
			return o.aiPermission
		case KindPreference:
			return o.preferences
		}
	}
	return true
}

// Sync 执行一轮完整同步: 对账 → 拉取合并 → 推送本地变更。冲突(其他设备先推或回滚)自动重试。
func (e *Engine) Sync(ctx context.Context, config RemoteConfig) (SyncReport, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	report := SyncReport{}
	userID := ""
	for attempt := 0; attempt < maxSyncAttempts; attempt++ {
		session, err := e.ensureSession(ctx, config)
		if err != nil {
			e.logSyncRound(userID, report, err)
			return report, err
		}
		userID = session.userID
		conflict, err := e.syncOnce(ctx, session, &report)
		if errors.Is(err, errSessionExpired) {
			e.dropSession(session.key)
			continue
		}
		if err != nil {
			e.logSyncRound(userID, report, err)
			return report, err
		}
		if !conflict {
			e.logSyncRound(userID, report, nil)
			return report, nil
		}
		report.Conflicts++
	}
	err := ipc.NewError(ipc.CodeForbidden, "同步冲突次数过多，请稍后重试")
	e.logSyncRound(userID, report, err)
	return report, err
}

// logSyncRound 每轮同步至多一行汇总: 空轮静默, 有变更 Info, 失败 Warn;
// 警告只记数量, 警告原文含对象 ID 不进日志。
func (e *Engine) logSyncRound(userID string, report SyncReport, syncErr error) {
	attrs := []any{
		"user", userID, "pulled", report.Pulled, "applied", report.Applied, "skipped", report.PullSkipped,
		"decrypt_failed", report.DecryptFailed, "pushed", report.Pushed, "conflicts", report.Conflicts,
		"warnings", len(report.Warnings),
	}
	if syncErr != nil {
		e.logger.Warn("sync round failed", append(attrs, "error", syncErr)...)
		return
	}
	if report.Pulled > 0 || report.Applied > 0 || report.PullSkipped > 0 ||
		report.DecryptFailed > 0 || report.Pushed > 0 || report.Conflicts > 0 {
		e.logger.Info("sync round complete", attrs...)
	}
	if len(report.Warnings) > 0 {
		e.logger.Warn("sync round warnings", "user", userID, "warnings", len(report.Warnings))
	}
}

func (e *Engine) ensureSession(ctx context.Context, config RemoteConfig) (*remoteSession, error) {
	key := config.URL + "\x00" + config.Username
	e.sessionMu.Lock()
	cached := e.session
	e.sessionMu.Unlock()
	if cached != nil && cached.key == key {
		return cached, nil
	}
	client, err := newRemoteClient(config)
	if err != nil {
		return nil, err
	}
	outcome, err := client.login(ctx, config.Username, config.Password)
	if err != nil {
		return nil, err
	}
	if outcome.mfaTicket != "" {
		return nil, ipc.NewError(ipc.CodeForbidden, "账号已开启两步验证：请先在同步设置中完成验证")
	}
	if outcome.resetRequired {
		return nil, ipc.NewError(ipc.CodeForbidden, "账号密码已被管理员重置：请先在同步设置中完成密码重置")
	}
	return e.establishSession(ctx, key, client, config.Password)
}

// establishSession 在密码通道已通过后完成会话入职: 取身份与 DEK 信封、解包 DEK 并缓存会话。
func (e *Engine) establishSession(ctx context.Context, key string, client *remoteClient, password string) (*remoteSession, error) {
	userID, err := client.me(ctx)
	if err != nil {
		return nil, err
	}
	envelopes, err := client.dekEnvelopes(ctx)
	if err != nil {
		return nil, err
	}
	dek, err := vault.UnwrapUserDEK(password, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		return nil, err
	}
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
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
	optIn, err := e.loadKindOptIn(ctx, userID)
	if err != nil {
		return false, err
	}
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
	completed, err := e.pullAllAndApply(ctx, session, &cursor, unreconciled, report, optIn)
	if err != nil {
		return false, err
	}
	if !completed {
		// 对账未完成(stall): 持久化进度但本轮不得 collect/push, 不得采用最新 head 继续。
		if err := e.saveCursor(ctx, userID, cursor); err != nil {
			return false, err
		}
		return false, ipc.NewError(ipc.CodeDisconnected, "同步数据不完整（部分对象未返回），请稍后重试")
	}
	objects, err := e.collectLocalObjects(ctx, report, optIn, session.userID)
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
	if completed, err := e.pullAllAndApply(ctx, session, &cursor, nil, report, optIn); err != nil {
		return false, err
	} else if !completed {
		report.warnf("收尾拉取超过页数上限，剩余对象将在下一轮同步")
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
func (e *Engine) pullAllAndApply(ctx context.Context, session *remoteSession, cursor *syncCursor, unreconciled []string, report *SyncReport, optIn kindOptIn) (bool, error) {
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
			e.applyRemoteObject(ctx, session.userID, object, session.dek, report, optIn)
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
	state := query.Use(e.store.GORM()).SyncState
	row, err := state.WithContext(ctx).Where(state.UserID.Eq(userID), state.ObjectID.Eq(objectID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if row.PayloadHash != nil {
		payloadHash = string(row.PayloadHash)
	}
	return payloadHash, hex.EncodeToString(row.BlobHash), true, nil
}

func (e *Engine) syncStatePut(ctx context.Context, userID, objectID string, payloadHash *string, blobHash [32]byte) error {
	var payload []byte
	if payloadHash != nil {
		payload = []byte(*payloadHash)
	}
	if err := e.store.GORM().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "object_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"payload_hash", "blob_hash"}),
	}).Create(&store.SyncState{UserID: userID, ObjectID: objectID, PayloadHash: payload, BlobHash: blobHash[:]}).Error; err != nil {
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
	tombstone := query.Use(e.store.GORM()).SyncTombstone
	result := []syncTombstoneRow{}
	err := tombstone.WithContext(ctx).
		Select(tombstone.ID, tombstone.Kind, tombstone.DeletedAt).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Order(tombstone.ID).
		Scan(&result)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return result, nil
}

func (e *Engine) syncTombstoneGet(ctx context.Context, objectID string) (syncTombstoneRow, bool, error) {
	tombstone := query.Use(e.store.GORM()).SyncTombstone
	row, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(objectID)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return syncTombstoneRow{}, false, nil
	}
	if err != nil {
		return syncTombstoneRow{}, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return syncTombstoneRow{ID: row.ID, Kind: row.Kind, DeletedAt: row.DeletedAt}, true, nil
}

func (e *Engine) syncTombstonePut(ctx context.Context, objectID, kind string, deletedAt int64) error {
	if err := e.store.GORM().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"deleted_at": gorm.Expr(scalarMax(e.store.Backend()) + "(sync_tombstone.deleted_at, excluded.deleted_at)"),
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: conflictOwnerColumn(e.store.Backend(), "sync_tombstone"), Value: ipc.OwnerID(ctx)}}},
	}).Create(&store.SyncTombstone{ID: objectID, Kind: kind, DeletedAt: deletedAt, OwnerID: ipc.OwnerID(ctx)}).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

// snippetUpsert 与 store.GroupUpsert 同款语义: 按 ID 幂等写入并保留对端修订号,
// 并在同一事务清除同 ID 的删除墓碑: 较新片段胜过旧墓碑后不得留下可回滚对象的墓碑。
func (e *Engine) snippetUpsert(ctx context.Context, row store.SnippetRow) error {
	return e.store.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return e.snippetUpsertTx(ctx, tx, row)
	})
}

// snippetUpsertTx 是 snippetUpsert 的事务内版本, 供 ImportBundle 把一个对象类的多次写入并入同一事务。
func (e *Engine) snippetUpsertTx(ctx context.Context, tx *gorm.DB, row store.SnippetRow) error {
	row.OwnerID = ipc.OwnerID(ctx)
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"group_id", "name", "body", "sort", "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: conflictOwnerColumn(e.store.Backend(), "snippet"), Value: ipc.OwnerID(ctx)}}},
	}).Create(&row).Error; err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	tombstone := query.Use(tx).SyncTombstone
	if _, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(row.ID)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func isNotFound(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound
}
