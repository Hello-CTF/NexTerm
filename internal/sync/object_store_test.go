package sync

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func newObjectStoreUser(t *testing.T) (context.Context, *objectStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	accounts := account.New(db.DB())
	user, err := accounts.CreateUser(ctx, "sync-store-user-"+ids.New(), "test", "store-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, &objectStore{db: db.DB()}, user.ID
}

func TestObjectStoreGenesisHeadAndIdempotentPush(t *testing.T) {
	ctx, store, userID := newObjectStoreUser(t)
	if head, err := store.currentHead(ctx, userID); err != nil || head != genesisHead(userID) {
		t.Fatalf("genesis head=%q err=%v", head, err)
	}
	objects := []WireObject{{ID: "obj-a", Blob: []byte("blob-a")}, {ID: "obj-b", Blob: []byte("blob-b")}}
	applied, skipped, head, maxSeq, err := store.push(ctx, userID, genesisHead(userID), objects)
	if err != nil || applied != 2 || skipped != 0 || maxSeq != 2 {
		t.Fatalf("push applied=%d skipped=%d maxSeq=%d err=%v", applied, skipped, maxSeq, err)
	}
	if head == genesisHead(userID) {
		t.Fatal("head did not advance")
	}
	// 同内容重复推送: 幂等跳过, head 与 seq 不变。
	applied, skipped, head2, maxSeq2, err := store.push(ctx, userID, head, objects)
	if err != nil || applied != 0 || skipped != 2 || head2 != head || maxSeq2 != maxSeq {
		t.Fatalf("idempotent push applied=%d skipped=%d head=%v seq=%d err=%v", applied, skipped, head2 == head, maxSeq2, err)
	}
}

func TestObjectStoreHeadMismatchDetectsFork(t *testing.T) {
	ctx, store, userID := newObjectStoreUser(t)
	genesis := genesisHead(userID)
	if _, _, _, _, err := store.push(ctx, userID, genesis, []WireObject{{ID: "obj-a", Blob: []byte("v1")}}); err != nil {
		t.Fatal(err)
	}
	// 基于过期 head 的推送必须被拒绝(回滚/分叉检测)。
	if _, _, _, _, err := store.push(ctx, userID, genesis, []WireObject{{ID: "obj-a", Blob: []byte("v2")}}); err != errHeadMismatch {
		t.Fatalf("stale head push err=%v, want errHeadMismatch", err)
	}
	// 对象内容被覆盖后 seq 单调递增。
	if _, _, _, _, err := store.push(ctx, userID, mustHead(t, store, ctx, userID), []WireObject{{ID: "obj-a", Blob: []byte("v2")}}); err != nil {
		t.Fatal(err)
	}
	entries, _, maxSeq, err := store.idList(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Seq != 2 || maxSeq != 2 {
		t.Fatalf("entries=%+v maxSeq=%d", entries, maxSeq)
	}
	if entries[0].BlobHash != hashBlobHex([]byte("v2")) {
		t.Fatal("blob hash mismatch")
	}
}

func mustHead(t *testing.T, store *objectStore, ctx context.Context, userID string) string {
	t.Helper()
	head, err := store.currentHead(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func TestObjectStorePullCursorAndIDLookup(t *testing.T) {
	ctx, store, userID := newObjectStoreUser(t)
	genesis := genesisHead(userID)
	objects := []WireObject{{ID: "obj-a", Blob: []byte("a")}, {ID: "obj-b", Blob: []byte("b")}, {ID: "obj-c", Blob: []byte("c")}}
	if _, _, _, _, err := store.push(ctx, userID, genesis, objects); err != nil {
		t.Fatal(err)
	}

	pulled, _, maxSeq, _, done, err := store.pull(ctx, userID, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 2 || pulled[0].ID != "obj-b" || pulled[1].ID != "obj-c" || !done || maxSeq != 3 {
		t.Fatalf("pulled=%+v done=%v maxSeq=%d", pulled, done, maxSeq)
	}
	// 按 id 补拉返回游标之外的对象。
	pulled, _, _, _, _, err = store.pull(ctx, userID, 3, []string{"obj-a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "obj-a" {
		t.Fatalf("id lookup pulled=%+v", pulled)
	}
	// 字节预算: 单对象必完整返回。
	pulled, _, _, _, done, err = store.pull(ctx, userID, 0, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "obj-a" || done {
		t.Fatalf("budget pull=%+v done=%v", pulled, done)
	}
}

func TestObjectStoreMultiUserIsolation(t *testing.T) {
	ctx, store, userA := newObjectStoreUser(t)
	_, storeB, userB := newObjectStoreUser(t)
	if _, _, _, _, err := store.push(ctx, userA, genesisHead(userA), []WireObject{{ID: "obj-a", Blob: []byte("alice")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := storeB.push(ctx, userB, genesisHead(userB), []WireObject{{ID: "obj-a", Blob: []byte("bob")}}); err != nil {
		t.Fatal(err)
	}
	entriesA, _, _, err := store.idList(ctx, userA)
	if err != nil {
		t.Fatal(err)
	}
	entriesB, _, _, err := storeB.idList(ctx, userB)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesA) != 1 || entriesA[0].BlobHash != hashBlobHex([]byte("alice")) {
		t.Fatalf("userA entries=%+v", entriesA)
	}
	if len(entriesB) != 1 || entriesB[0].BlobHash != hashBlobHex([]byte("bob")) {
		t.Fatalf("userB entries=%+v", entriesB)
	}
	pulled, _, _, _, _, err := store.pull(ctx, userA, 0, []string{"obj-a"}, 0)
	if err != nil || len(pulled) != 1 || string(pulled[0].Blob) != "alice" {
		t.Fatalf("userA pulled=%+v err=%v", pulled, err)
	}
}

// 两条接近 64MiB 的 transcript(密文约 87MiB)必须被线上成本预算分页, 单条响应不超上限。
func TestObjectStorePullWireBudgetPaginatesLargeObjects(t *testing.T) {
	ctx, store, userID := newObjectStoreUser(t)
	big := make([]byte, 87<<20)
	if _, err := rand.Read(big[:1024]); err != nil {
		t.Fatal(err)
	}
	objects := []WireObject{{ID: "obj-big-1", Blob: big}, {ID: "obj-big-2", Blob: bytes.Clone(big)}}
	if _, _, _, _, err := store.push(ctx, userID, genesisHead(userID), objects); err != nil {
		t.Fatal(err)
	}
	cost := wireObjectCost(big)
	if cost <= maxPullWireBytes/2 || cost > maxPullWireBytes {
		t.Fatalf("wireObjectCost(%d MiB)=%d, 期望介于半数与满额预算之间", len(big)>>20, cost)
	}

	pulled, _, maxSeq, _, done, err := store.pull(ctx, userID, 0, nil, maxPullWireBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "obj-big-1" || done {
		t.Fatalf("first page=%+v done=%v, want only obj-big-1 and not done", pulled, done)
	}
	if wireObjectCost(pulled[0].Blob) > maxPullWireBytes {
		t.Fatalf("first page wire cost %d exceeds budget", wireObjectCost(pulled[0].Blob))
	}
	pulled, _, maxSeq, _, done, err = store.pull(ctx, userID, pulled[0].Seq, nil, maxPullWireBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "obj-big-2" || !done || maxSeq != 2 {
		t.Fatalf("second page=%+v done=%v maxSeq=%d, want obj-big-2 and done", pulled, done, maxSeq)
	}
}
