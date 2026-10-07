package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// pushAllConcurrently 模拟多设备同时推送: 每个 worker 推送一批互不相同对象,
// head 冲突时重读重试。所有批次必须最终全部应用。
func pushAllConcurrently(t *testing.T, ctx context.Context, objectStore *objectStore, userID string, workers, perWorker int) string {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			objects := make([]WireObject, 0, perWorker)
			for i := 0; i < perWorker; i++ {
				objects = append(objects, WireObject{ID: fmt.Sprintf("worker-%d-obj-%d", w, i), Blob: []byte(fmt.Sprintf("blob-%d-%d", w, i))})
			}
			for attempt := 0; attempt < 200; attempt++ {
				head, err := objectStore.currentHead(ctx, userID)
				if err != nil {
					errs <- err
					return
				}
				if _, _, _, _, err := objectStore.push(ctx, userID, head, objects); err == nil {
					return
				} else if !errors.Is(err, errHeadMismatch) {
					errs <- err
					return
				}
			}
			errs <- fmt.Errorf("worker %d: push retries exhausted", w)
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	head, err := objectStore.currentHead(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// assertSeqContiguous 校验并发分配结果: 对象齐全, seq 从 1 起连续且不重复。
func assertSeqContiguous(t *testing.T, ctx context.Context, objectStore *objectStore, userID string, total int) {
	t.Helper()
	entries, _, maxSeq, err := objectStore.idList(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != total || maxSeq != int64(total) {
		t.Fatalf("entries=%d maxSeq=%d want %d", len(entries), maxSeq, total)
	}
	seen := make(map[int64]string, total)
	for _, entry := range entries {
		if prev, dup := seen[entry.Seq]; dup {
			t.Fatalf("duplicate seq %d (%s vs %s)", entry.Seq, prev, entry.ID)
		}
		seen[entry.Seq] = entry.ID
	}
	for seq := int64(1); seq <= int64(total); seq++ {
		if _, ok := seen[seq]; !ok {
			t.Fatalf("seq %d missing", seq)
		}
	}
}

// SQLite 并发推送: 多连接文件库下 head/seq 分配必须串行化, 无重复 seq, 无对象丢失。
func TestObjectStoreConcurrentPushSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync-concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user, err := account.New(db.DB()).CreateUser(ctx, "sync-concurrent-"+ids.New(), "test", "store-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	objectStore := &objectStore{db: db.DB(), backend: db.Backend()}

	head := pushAllConcurrently(t, ctx, objectStore, user.ID, 8, 4)
	if head == genesisHead(user.ID) {
		t.Fatal("head did not advance")
	}
	assertSeqContiguous(t, ctx, objectStore, user.ID, 32)
}
