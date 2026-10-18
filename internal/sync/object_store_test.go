package sync

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func newObjectStoreUser(t *testing.T) (context.Context, *objectStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	accounts := account.New(db.GORM())
	user, err := accounts.CreateUser(ctx, "sync-store-user-"+ids.New(), "test", "store-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, &objectStore{db: db.GORM()}, user.ID
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
	// Re-pushing the same content: idempotent skip, head and seq unchanged.
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
	// A push based on a stale head must be rejected (rollback/fork detection).
	if _, _, _, _, err := store.push(ctx, userID, genesis, []WireObject{{ID: "obj-a", Blob: []byte("v2")}}); err != errHeadMismatch {
		t.Fatalf("stale head push err=%v, want errHeadMismatch", err)
	}
	// Seq increases monotonically after object content is overwritten.
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
	// Catch-up by id returns objects beyond the cursor.
	pulled, _, _, _, _, err = store.pull(ctx, userID, 3, []string{"obj-a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "obj-a" {
		t.Fatalf("id lookup pulled=%+v", pulled)
	}
	// Byte budget: a single object is always returned in full.
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

func TestObjectStoreConditionalDeletePreservesCursorAndIsolation(t *testing.T) {
	ctx, objectStore, userID := newObjectStoreUser(t)
	other, err := account.New(objectStore.db).CreateUser(ctx, "sync-other-"+ids.New(), "test", "store-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	_, _, head, _, err := objectStore.push(ctx, userID, genesisHead(userID), []WireObject{
		{ID: "keep", Blob: []byte("keep-data")}, {ID: "broken", Blob: []byte("opaque-data")},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := hashBlobHex([]byte("opaque-data"))
	for _, attempt := range []struct {
		user, knownHead, blobHash string
	}{
		{userID, genesisHead(userID), hash},
		{userID, head, hashBlobHex([]byte("different"))},
		{other.ID, head, hash},
	} {
		if _, _, err := objectStore.deleteObject(ctx, attempt.user, "broken", attempt.knownHead, attempt.blobHash); err != errHeadMismatch {
			t.Fatalf("conditional delete err = %v, want conflict", err)
		}
	}
	entries, unchangedHead, unchangedSeq, err := objectStore.idList(ctx, userID)
	if err != nil || len(entries) != 2 || unchangedHead != head || unchangedSeq != 2 {
		t.Fatalf("conflicting delete changed store: entries=%+v head=%q maxSeq=%d err=%v", entries, unchangedHead, unchangedSeq, err)
	}

	newHead, maxSeq, err := objectStore.deleteObject(ctx, userID, "broken", head, hash)
	if err != nil || newHead == head || maxSeq != 3 {
		t.Fatalf("delete head=%q maxSeq=%d err=%v", newHead, maxSeq, err)
	}
	entries, listedHead, listedMax, err := objectStore.idList(ctx, userID)
	if err != nil || len(entries) != 1 || entries[0].ID != "keep" || listedHead != newHead || listedMax != 3 {
		t.Fatalf("deleted object remains in ids: entries=%+v head=%q maxSeq=%d err=%v", entries, listedHead, listedMax, err)
	}
	pulled, _, pulledMax, nextSeq, done, err := objectStore.pull(ctx, userID, 0, []string{"broken"}, 0)
	if err != nil || len(pulled) != 1 || pulled[0].ID != "keep" || pulledMax != 3 || nextSeq != 3 || !done {
		t.Fatalf("deleted object remains in pull: objects=%+v maxSeq=%d nextSeq=%d done=%v err=%v", pulled, pulledMax, nextSeq, done, err)
	}
	pulled, observedHead, pulledMax, nextSeq, done, err := objectStore.pull(ctx, userID, 2, nil, 0)
	if err != nil || len(pulled) != 0 || observedHead != newHead || pulledMax != 3 || nextSeq != 3 || !done {
		t.Fatalf("old cursor did not observe clear: objects=%+v head=%q maxSeq=%d nextSeq=%d done=%v err=%v", pulled, observedHead, pulledMax, nextSeq, done, err)
	}
	if _, _, err := objectStore.deleteObject(ctx, userID, "broken", newHead, hash); err != errHeadMismatch {
		t.Fatalf("repeat delete err=%v, want conflict", err)
	}
	_, _, pushedHead, pushedSeq, err := objectStore.push(ctx, userID, newHead, []WireObject{{ID: "new", Blob: []byte("new-data")}})
	if err != nil || pushedSeq != 4 || pushedHead == newHead {
		t.Fatalf("push after delete head=%q maxSeq=%d err=%v", pushedHead, pushedSeq, err)
	}
	pulled, _, _, nextSeq, done, err = objectStore.pull(ctx, userID, 3, nil, 0)
	if err != nil || len(pulled) != 1 || pulled[0].ID != "new" || nextSeq != 4 || !done {
		t.Fatalf("old cursor missed new push: objects=%+v nextSeq=%d done=%v err=%v", pulled, nextSeq, done, err)
	}
}

// Two near-64MiB transcripts (ciphertext about 87MiB) must be paginated by the wire-cost budget, with no single response over the limit.
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
		t.Fatalf("wireObjectCost(%d MiB)=%d, want between half and full budget", len(big)>>20, cost)
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
