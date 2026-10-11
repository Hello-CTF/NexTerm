package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errHeadMismatch = errors.New("sync head mismatch")

const (
	headAADPrefix    = "nexterm/go/sync-head/v1"
	maxPushObjects   = 4096
	maxPullIDLookup  = 4096
	maxIDListEntries = 200000
	maxPullWireBytes = 128 << 20
)

func wireObjectCost(blob []byte) int64 {
	return int64((len(blob)+2)/3*4) + 256
}

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
	db      *gorm.DB
	backend store.Backend
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
	head := query.Use(o.db).UserSyncHead
	row, err := head.WithContext(ctx).Where(head.UserID.Eq(userID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return genesisHead(userID), nil
	}
	if err != nil {
		return "", ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return row.HeadHash, nil
}

func (o *objectStore) maxSeq(ctx context.Context, userID string) (int64, error) {
	object := query.Use(o.db).UserSyncObject
	var maxSeq int64
	if err := object.WithContext(ctx).
		Where(object.UserID.Eq(userID)).
		UnderlyingDB().
		Select("COALESCE(MAX(seq), 0)").
		Scan(&maxSeq).Error; err != nil {
		return 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return maxSeq, nil
}

type appliedEntry struct {
	seq      int64
	id       string
	blobHash string
}

func (o *objectStore) push(ctx context.Context, userID, knownHead string, objects []WireObject) (applied, skipped int, head string, maxSeq int64, returnErr error) {
	if len(objects) == 0 {
		current, err := o.currentHead(ctx, userID)
		return 0, 0, current, 0, err
	}
	if len(objects) > maxPushObjects {
		return 0, 0, "", 0, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("单批推送对象数超过 %d", maxPushObjects))
	}
	err := o.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		headModel := query.Use(tx).UserSyncHead
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoNothing: true}).
			Create(&store.UserSyncHead{UserID: userID, HeadHash: genesisHead(userID)}).Error; err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		q := headModel.WithContext(ctx).Where(headModel.UserID.Eq(userID))
		if o.backend == store.BackendPostgres {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		row, err := q.First()
		if err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		current := row.HeadHash
		if current != knownHead {
			return errHeadMismatch
		}
		objectModel := query.Use(tx).UserSyncObject
		if err := objectModel.WithContext(ctx).
			Where(objectModel.UserID.Eq(userID)).
			UnderlyingDB().
			Select("COALESCE(MAX(seq), 0)").
			Scan(&maxSeq).Error; err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		entries := make([]appliedEntry, 0, len(objects))
		seen := make(map[string]bool, len(objects))
		for _, object := range objects {
			if !validObjectID(object.ID) {
				return ipc.NewError(ipc.CodeBadParam, "同步对象 ID 不合法")
			}
			if len(object.Blob) == 0 || len(object.Blob) > maxSyncObjectBytes {
				return ipc.NewError(ipc.CodeBadParam, "同步对象大小超出限制")
			}
			if seen[object.ID] {
				return ipc.NewError(ipc.CodeBadParam, "同一批推送包含重复对象 ID")
			}
			seen[object.ID] = true
			existing, err := objectModel.WithContext(ctx).
				Where(objectModel.UserID.Eq(userID), objectModel.ID.Eq(object.ID)).
				First()
			switch {
			case err == nil && bytes.Equal(existing.Blob, object.Blob):
				skipped++
				continue
			case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
			maxSeq++
			blobHash := hashBlob(object.Blob)
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}, {Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"seq", "blob", "blob_hash"}),
			}).Create(&store.UserSyncObject{UserID: userID, ID: object.ID, Seq: maxSeq, Blob: object.Blob, BlobHash: blobHash[:]}).Error; err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
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
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"head_hash"}),
			}).Create(&store.UserSyncHead{UserID: userID, HeadHash: current}).Error; err != nil {
				return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
			}
		}
		applied = len(entries)
		head = current
		return nil
	})
	if err != nil {
		if errors.Is(err, errHeadMismatch) {
			return 0, 0, "", 0, errHeadMismatch
		}
		return 0, 0, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return applied, skipped, head, maxSeq, nil
}

func (o *objectStore) pull(ctx context.Context, userID string, sinceSeq int64, ids []string, maxBytes int64) (objects []WireObjectSeq, head string, maxSeq, nextSeq int64, cursorDone bool, returnErr error) {
	if len(ids) > maxPullIDLookup {
		return nil, "", 0, 0, false, ipc.NewError(ipc.CodeBadParam, fmt.Sprintf("按 ID 拉取的对象数超过 %d", maxPullIDLookup))
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
	objectModel := query.Use(o.db).UserSyncObject
	rows := []store.UserSyncObject{}
	if err := objectModel.WithContext(ctx).
		Where(objectModel.UserID.Eq(userID), objectModel.Seq.Gt(sinceSeq)).
		Order(objectModel.Seq).
		Scan(&rows); err != nil {
		return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	for _, row := range rows {
		object := WireObjectSeq{ID: row.ID, Seq: row.Seq, Blob: row.Blob}
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
	for _, id := range ids {
		if seen[id] {
			continue
		}
		row, err := objectModel.WithContext(ctx).
			Where(objectModel.UserID.Eq(userID), objectModel.ID.Eq(id)).
			First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, "", 0, 0, false, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		object := WireObjectSeq{ID: row.ID, Seq: row.Seq, Blob: row.Blob}
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

func (o *objectStore) idList(ctx context.Context, userID string) (entries []IDEntry, head string, maxSeq int64, returnErr error) {
	head, err := o.currentHead(ctx, userID)
	if err != nil {
		return nil, "", 0, err
	}
	if maxSeq, err = o.maxSeq(ctx, userID); err != nil {
		return nil, "", 0, err
	}
	objectModel := query.Use(o.db).UserSyncObject
	rows := []store.UserSyncObject{}
	if err := objectModel.WithContext(ctx).
		Where(objectModel.UserID.Eq(userID)).
		Order(objectModel.Seq).
		Limit(maxIDListEntries + 1).
		Scan(&rows); err != nil {
		return nil, "", 0, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	entries = []IDEntry{}
	for _, row := range rows {
		entries = append(entries, IDEntry{ID: row.ID, Seq: row.Seq, BlobHash: hex.EncodeToString(row.BlobHash)})
	}
	if len(entries) > maxIDListEntries {
		return nil, "", 0, ipc.NewError(ipc.CodeBadParam, "同步对象数量超出清单上限")
	}
	return entries, head, maxSeq, nil
}
