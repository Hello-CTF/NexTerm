package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// f/x/h counterexample: a catch-up ID's high seq must not advance the cursor past unreturned low-seq cursor objects,
// next_seq/cursor_done may only be computed from cursor objects.
func TestObjectStoreCursorProgressIndependentOfIDLookup(t *testing.T) {
	ctx, store, userID := newObjectStoreUser(t)
	blobs := []WireObject{
		{ID: "f", Blob: []byte("small-f")},
		{ID: "x", Blob: make([]byte, 3<<20)},
		{ID: "h", Blob: []byte("small-h")},
	}
	if _, _, _, _, err := store.push(ctx, userID, genesisHead(userID), blobs); err != nil {
		t.Fatal(err)
	}
	// The budget only fits f plus catch-up h: x is skipped by the budget, but next_seq must stop at f's seq with cursor_done=false.
	pulled, _, maxSeq, nextSeq, cursorDone, err := store.pull(ctx, userID, 0, []string{"h"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 2 || pulled[0].ID != "f" || pulled[1].ID != "h" {
		t.Fatalf("pulled=%+v, want [f h]", pulled)
	}
	if nextSeq != 1 {
		t.Fatalf("nextSeq=%d, want 1 (cursor objects only, not h's 3)", nextSeq)
	}
	if cursorDone {
		t.Fatal("cursorDone=true with cursor seq2 missing; id-lookup seq must not fake cursor completion")
	}
	// Later pages must still return x, advanced only by the cursor.
	pulled, _, _, nextSeq, cursorDone, err = store.pull(ctx, userID, nextSeq, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "x" || nextSeq != 2 || cursorDone {
		t.Fatalf("second page=%+v nextSeq=%d cursorDone=%v, want [x] seq2 not done", pulled, nextSeq, cursorDone)
	}
	pulled, _, _, nextSeq, cursorDone, err = store.pull(ctx, userID, nextSeq, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 1 || pulled[0].ID != "h" || nextSeq != 3 || !cursorDone || maxSeq != 3 {
		t.Fatalf("third page=%+v nextSeq=%d cursorDone=%v maxSeq=%d, want [h] seq3 done", pulled, nextSeq, cursorDone, maxSeq)
	}
}

// x updated concurrently after ids: the engine must pull x back via the cursor stream and complete LWW; the stale local revision must not overwrite the newer remote revision.
func TestEngineCursorPullPreventsOverwrite(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	bigBody := strings.Repeat("x", 3<<20)
	groupID := ids.New()
	putDeviceGroup(t, deviceA, groupID, nil, "f", 100)
	snippetX := ids.New()
	putDeviceSnippet(t, deviceA, snippetX, "x", bigBody, 100)
	snippetH := ids.New()
	putDeviceSnippet(t, deviceA, snippetH, "h", "old-h", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A has a pending push of an old-revision edit (older than the revision B will create, but different from the synced local version).
	putDeviceSnippet(t, deviceA, snippetX, "x", "stale-local-"+bigBody, 200)

	// B updates x and h to a higher revision and pushes only after the ids response: x is not in A's unreconciled,
	// so it can only be pulled back by the cursor stream in a later page; the old client would skip x via catch-up h's high seq and overwrite it.
	future := ids.NowMS() + 3600_000
	server.afterIDs = func() {
		putDeviceSnippet(t, deviceB, snippetX, "x", "new-"+bigBody, future)
		putDeviceSnippet(t, deviceB, snippetH, "h", "new-h", future)
		syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	}

	// Shrink the single-page budget to force x into the next page; the engine must exhaust pagination and LWW, never overwriting B's new revision with stale-local.
	original := maxPullBytes
	maxPullBytes = 1 << 20
	defer func() { maxPullBytes = original }()
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	rows, err := deviceA.db.SnippetList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, row := range rows {
		contents[row.Name] = row.Body
	}
	if contents["x"] != "new-"+bigBody {
		t.Fatal("deviceA must keep B's new revision of x, not the stale local edit")
	}
	if contents["h"] != "new-h" {
		t.Fatal("deviceA must keep B's new revision of h")
	}
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pulled+reportA.Applied+reportA.Pushed != 0 {
		t.Fatalf("deviceA idle sync not quiescent: %+v", reportA)
	}
}

// stall: while a catch-up ID stays missing, the round must exit before collect/push with zero pushes.
func TestEngineStallPreventsPush(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	groupID := ids.New()
	putDeviceGroup(t, deviceA, groupID, nil, "A 分组", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A updates the group, producing a new blob hash that puts the object into B's unreconciled; B also has a pending push.
	putDeviceGroup(t, deviceA, groupID, nil, "A 分组-新版", 300)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	putDeviceGroup(t, deviceB, groupID, nil, "B 待推分组", 200)

	// The server drops the object row after ids, so the catch-up stays missing: the round must stall with zero pushes.
	server.dropObjectID = groupID
	report, err := deviceB.engine.Sync(ctx, deviceB.config(server, "alice", "alice-pw-123"))
	if err == nil {
		t.Fatal("stall must surface an error, not a successful reconcile")
	}
	if report.Pushed != 0 {
		t.Fatalf("stall must prevent any push this round: %+v", report)
	}
	// The server object row was dropped, so B's pending object was not pushed over it.
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM user_sync_object").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("server objects=%d, want 0 after drop", count)
	}
}

// More than 4096 unreconciled IDs must all be reconciled via inner batching and must not be truncated or dropped by the outer layer.
func TestEngineLargeUnreconciledSetConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	// A establishes the session first, then pushes 4096+500 objects directly.
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	userID := deviceA.engine.session.userID
	const total = maxPullIDLookup + 500
	objects := make([]WireObject, 0, total)
	for i := 0; i < total; i++ {
		objects = append(objects, WireObject{ID: ids.New(), Blob: []byte("obj")})
	}
	store := &objectStore{db: server.db.GORM()}
	head, err := store.currentHead(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < total; start += maxPushObjects {
		end := start + maxPushObjects
		if end > total {
			end = total
		}
		if _, _, head, _, err = store.push(ctx, userID, head, objects[start:end]); err != nil {
			t.Fatal(err)
		}
	}

	// B has reconciled none of the objects (unreconciled exceeds 4096); all must be pulled back before push is allowed.
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM user_sync_object WHERE user_id = ?", userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != total {
		t.Fatalf("server objects=%d, want %d", count, total)
	}
	// B's sync_state must cover every object (nothing truncated or dropped).
	var synced int
	if err := deviceB.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM sync_state WHERE user_id = ?", deviceB.engine.session.userID).Scan(&synced); err != nil {
		t.Fatal(err)
	}
	if synced != total {
		t.Fatalf("deviceB synced=%d, want %d (no truncation drop)", synced, total)
	}
}
