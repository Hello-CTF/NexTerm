package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// f/x/h 反例: 补拉 ID 的高 seq 不得把游标推进跳过未返回的低 seq 游标对象,
// next_seq/cursor_done 只能由游标对象计算。
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
	// 预算只容纳 f 与补拉 h: x 被预算跳过, 但 next_seq 必须停在 f 的 seq, cursor_done=false。
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
	// 后续页必须仍返回 x 并仅由游标推进。
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

// ids 后并发更新 x: 引擎必须经游标流拉回 x 并完成 LWW, 本地旧 revision 不得覆盖远端新 revision。
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

	// A 有一个待推送的旧 revision 修改(比 B 将创建的 revision 旧, 但与本地已同步版本不同)。
	putDeviceSnippet(t, deviceA, snippetX, "x", "stale-local-"+bigBody, 200)

	// ids 响应之后 B 才更新 x 与 h 到更高 revision 并推送: x 不在 A 的 unreconciled 中,
	// 只能由游标流在分页后续页拉回; 旧客户端会用补拉 h 的高 seq 跳过 x 并覆盖它。
	future := ids.NowMS() + 3600_000
	server.afterIDs = func() {
		putDeviceSnippet(t, deviceB, snippetX, "x", "new-"+bigBody, future)
		putDeviceSnippet(t, deviceB, snippetH, "h", "new-h", future)
		syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	}

	// 调小单页预算强制 x 进入下一页; 引擎必须耗尽分页并 LWW, 不得用 stale-local 覆盖 B 的新 revision。
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

// stall: 补拉 ID 持续缺失时, 本轮必须在 collect/push 前退出, push 次数为零。
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

	// A 更新分组产生新 blob 哈希, 使 B 端该对象进入 unreconciled; B 同时有一个待推送修改。
	putDeviceGroup(t, deviceA, groupID, nil, "A 分组-新版", 300)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	putDeviceGroup(t, deviceB, groupID, nil, "B 待推分组", 200)

	// ids 之后服务端丢弃该对象行, 补拉持续缺失: 本轮必须 stall 且零推送。
	server.dropObjectID = groupID
	report, err := deviceB.engine.Sync(ctx, deviceB.config(server, "alice", "alice-pw-123"))
	if err == nil {
		t.Fatal("stall must surface an error, not a successful reconcile")
	}
	if report.Pushed != 0 {
		t.Fatalf("stall must prevent any push this round: %+v", report)
	}
	// 服务端对象行已被丢弃, B 的待推对象未被覆盖性推送。
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM user_sync_object").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("server objects=%d, want 0 after drop", count)
	}
}

// 超过 4096 个 unreconciled ID 必须全部经内层分批对账, 不得被外层截断丢弃。
func TestEngineLargeUnreconciledSetConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	// A 先建立会话, 然后直接推送 4096+500 个对象。
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	userID := deviceA.engine.session.userID
	const total = maxPullIDLookup + 500
	objects := make([]WireObject, 0, total)
	for i := 0; i < total; i++ {
		objects = append(objects, WireObject{ID: ids.New(), Blob: []byte("obj")})
	}
	store := &objectStore{db: server.db.DB(), backend: server.db.Backend()}
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

	// B 对全部对象未对账(unreconciled 超过 4096), 必须全部拉回后才允许 push。
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM user_sync_object WHERE user_id = ?", userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != total {
		t.Fatalf("server objects=%d, want %d", count, total)
	}
	// B 端 sync_state 必须覆盖全部对象(无被截断丢弃)。
	var synced int
	if err := deviceB.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM sync_state WHERE user_id = ?", deviceB.engine.session.userID).Scan(&synced); err != nil {
		t.Fatal(err)
	}
	if synced != total {
		t.Fatalf("deviceB synced=%d, want %d (no truncation drop)", synced, total)
	}
}
