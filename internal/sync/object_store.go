package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// errHeadMismatch 表示推送所基于的 head 与服务端当前 head 不一致:
// 其他设备先推送过, 或服务端数据被回滚/分叉。客户端必须先拉取合并后重试。
var errHeadMismatch = errors.New("sync head mismatch")

const (
	headAADPrefix    = "nexterm/go/sync-head/v1"
	maxPushObjects   = 4096
	maxPullIDLookup  = 4096
	maxIDListEntries = 200000
	// maxPullWireBytes 是拉取响应的线上(JSON/base64)预算上限, 服务端分页与客户端读取共用。
	maxPullWireBytes = 128 << 20
)

// wireObjectCost 估算单个对象在线上响应中的 JSON/base64 成本, 作为分页预算单位。
func wireObjectCost(blob []byte) int64 {
	return int64((len(blob)+2)/3*4) + 256
}

// WireObject 是线上传输的密文对象; 服务端不解读 blob 内容。
type WireObject struct {
	ID   string `json:"id"`
	Blob []byte `json:"blob"`
}

type WireObjectSeq struct {
	ID   string `json:"id"`
	Seq  int64  `json:"seq"`
	Blob []byte `json:"blob"`
}

type IDEntry struct {
	ID       string `json:"id"`
	Seq      int64  `json:"seq"`
	BlobHash string `json:"blob_hash"`
}

func genesisHead(userID string) string {
	digest := sha256.Sum256([]byte(headAADPrefix + "\x00" + userID))
	return hex.EncodeToString(digest[:])
}

func hashBlob(blob []byte) [32]byte {
	return sha256.Sum256(blob)
}

func hashBlobHex(blob []byte) string {
	digest := hashBlob(blob)
	return hex.EncodeToString(digest[:])
}

type objectStore struct {
	db *sql.DB
}

func validObjectID(id string) bool {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (o *objectStore) currentHead(ctx context.Context, userID string) (string, error) {
	var head string
	err := o.db.QueryRowContext(ctx, "SELECT head_hash FROM user_sync_head WHERE user_id = ?", userID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return genesisHead(userID), nil
	}
	if err != nil {
		return "", ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return head, nil
}

func (o *objectStore) maxSeq(ctx context.Context, userID string) (int64, error) {
	var maxSeq sql.NullInt64
	if err := o.db.QueryRowContext(ctx, "SELECT MAX(seq) FROM user_sync_object WHERE user_id = ?", userID).Scan(&maxSeq); err != nil {
		return 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return maxSeq.Int64, nil
}

type appliedEntry struct {
	seq      int64
	id       string
	blobHash string
}

// push 原子应用一批密文对象: head 比对失败整体拒绝; 同内容重复推送幂等跳过。
func (o *objectStore) push(ctx context.Context, userID, knownHead string, objects []WireObject) (applied, skipped int, head string, maxSeq int64, returnErr error) {
	if len(objects) == 0 {
		current, err := o.currentHead(ctx, userID)
		return 0, 0, current, 0, err
	}
	if len(objects) > maxPushObjects {
		return 0, 0, "", 0, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("单批推送对象数超过 %d", maxPushObjects))
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	var current string
	err = tx.QueryRowContext(ctx, "SELECT head_hash FROM user_sync_head WHERE user_id = ?", userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = genesisHead(userID)
	} else if err != nil {
		return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if current != knownHead {
		return 0, 0, "", 0, errHeadMismatch
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM user_sync_object WHERE user_id = ?", userID).Scan(&maxSeq); err != nil {
		return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	entries := make([]appliedEntry, 0, len(objects))
	seen := make(map[string]bool, len(objects))
	for _, object := range objects {
		if !validObjectID(object.ID) {
			return 0, 0, "", 0, ipc.NewError(ipc.CodeBadParam, "同步对象 ID 不合法")
		}
		if len(object.Blob) == 0 || len(object.Blob) > maxSyncObjectBytes {
			return 0, 0, "", 0, ipc.NewError(ipc.CodeBadParam, "同步对象大小超出限制")
		}
		if seen[object.ID] {
			return 0, 0, "", 0, ipc.NewError(ipc.CodeBadParam, "同一批推送包含重复对象 ID")
		}
		seen[object.ID] = true
		var existing []byte
		err := tx.QueryRowContext(ctx, "SELECT blob FROM user_sync_object WHERE user_id = ? AND id = ?", userID, object.ID).Scan(&existing)
		switch {
		case err == nil && bytes.Equal(existing, object.Blob):
			skipped++
			continue
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		maxSeq++
		blobHash := hashBlob(object.Blob)
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_sync_object(user_id, id, seq, blob, blob_hash) VALUES(?,?,?,?,?)
ON CONFLICT(user_id, id) DO UPDATE SET seq = excluded.seq, blob = excluded.blob, blob_hash = excluded.blob_hash`,
			userID, object.ID, maxSeq, object.Blob, blobHash[:]); err != nil {
			return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		entries = append(entries, appliedEntry{seq: maxSeq, id: object.ID, blobHash: hashBlobHex(object.Blob)})
	}
	if len(entries) > 0 {
		sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
		batch := sha256.New()
		for _, entry := range entries {
			fmt.Fprintf(batch, "%d\x00%s\x00%s\n", entry.seq, entry.id, entry.blobHash)
		}
		sum := sha256.Sum256(append([]byte(current+"\x00"), batch.Sum(nil)...))
		current = hex.EncodeToString(sum[:])
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_sync_head(user_id, head_hash) VALUES(?,?)
ON CONFLICT(user_id) DO UPDATE SET head_hash = excluded.head_hash`, userID, current); err != nil {
			return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return len(entries), skipped, current, maxSeq, nil
}

// pull 按 seq 游标升序返回密文对象, 另可按 id 清单补拉; 受字节预算约束, 单对象必完整返回。
// nextSeq/cursorDone 仅由游标对象计算: 补拉 ID 的 seq 不得推进游标或伪造游标完成状态。
func (o *objectStore) pull(ctx context.Context, userID string, sinceSeq int64, ids []string, maxBytes int64) (objects []WireObjectSeq, head string, maxSeq, nextSeq int64, cursorDone bool, returnErr error) {
	if len(ids) > maxPullIDLookup {
		return nil, "", 0, 0, false, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("按 ID 补拉数量超过 %d", maxPullIDLookup))
	}
	if maxBytes <= 0 || maxBytes > maxPullWireBytes {
		maxBytes = maxPullWireBytes
	}
	head, err := o.currentHead(ctx, userID)
	if err != nil {
		return nil, "", 0, 0, false, err
	}
	if maxSeq, err = o.maxSeq(ctx, userID); err != nil {
		return nil, "", 0, 0, false, err
	}
	objects = []WireObjectSeq{}
	seen := map[string]bool{}
	budget := maxBytes
	nextSeq = sinceSeq
	rows, err := o.db.QueryContext(ctx, "SELECT id, seq, blob FROM user_sync_object WHERE user_id = ? AND seq > ? ORDER BY seq", userID, sinceSeq)
	if err != nil {
		return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer rows.Close()
	for rows.Next() {
		var object WireObjectSeq
		if err := rows.Scan(&object.ID, &object.Seq, &object.Blob); err != nil {
			return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		// 追加前按线上成本判断预算(首个对象除外): 保证响应整体不超过上限。
		if cost := wireObjectCost(object.Blob); len(objects) > 0 && cost > budget {
			break
		} else {
			budget -= cost
		}
		objects = append(objects, object)
		seen[object.ID] = true
		if object.Seq > nextSeq {
			nextSeq = object.Seq
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if err := rows.Close(); err != nil {
		return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		var object WireObjectSeq
		err := o.db.QueryRowContext(ctx, "SELECT id, seq, blob FROM user_sync_object WHERE user_id = ? AND id = ?", userID, id).
			Scan(&object.ID, &object.Seq, &object.Blob)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		if cost := wireObjectCost(object.Blob); len(objects) > 0 && cost > budget {
			continue
		} else {
			budget -= cost
		}
		objects = append(objects, object)
	}
	cursorDone = nextSeq >= maxSeq
	return objects, head, maxSeq, nextSeq, cursorDone, nil
}

// idList 返回每用户对象清单 (id, seq, blob 哈希), 供客户端不回拉密文即可对账。
func (o *objectStore) idList(ctx context.Context, userID string) (entries []IDEntry, head string, maxSeq int64, returnErr error) {
	head, err := o.currentHead(ctx, userID)
	if err != nil {
		return nil, "", 0, err
	}
	if maxSeq, err = o.maxSeq(ctx, userID); err != nil {
		return nil, "", 0, err
	}
	rows, err := o.db.QueryContext(ctx, "SELECT id, seq, blob_hash FROM user_sync_object WHERE user_id = ? ORDER BY seq LIMIT ?", userID, maxIDListEntries+1)
	if err != nil {
		return nil, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer rows.Close()
	entries = []IDEntry{}
	legacy := []string{}
	for rows.Next() {
		var entry IDEntry
		var blobHash []byte
		if err := rows.Scan(&entry.ID, &entry.Seq, &blobHash); err != nil {
			return nil, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		if len(blobHash) == 0 {
			legacy = append(legacy, entry.ID)
		} else {
			entry.BlobHash = hex.EncodeToString(blobHash)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if err := rows.Close(); err != nil {
		return nil, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if len(entries) > maxIDListEntries {
		return nil, "", 0, ipc.NewError(ipc.CodeBadParam, "同步对象数量超出清单上限")
	}
	for _, id := range legacy {
		var blob []byte
		if err := o.db.QueryRowContext(ctx, "SELECT blob FROM user_sync_object WHERE user_id = ? AND id = ?", userID, id).Scan(&blob); err != nil {
			return nil, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		for i := range entries {
			if entries[i].ID == id {
				entries[i].BlobHash = hashBlobHex(blob)
			}
		}
	}
	return entries, head, maxSeq, nil
}
