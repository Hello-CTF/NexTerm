package store

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func seedKnownHost(t *testing.T, db *Store, id string) {
	t.Helper()
	if err := db.KnownHostAccept(context.Background(), "host-"+id, 22, "ssh-ed25519", "SHA256:fp"); err != nil {
		t.Fatal(err)
	}
}

func knownHostTombstone(t *testing.T, db *Store, id string) (kind string, deletedAt int64, found bool) {
	t.Helper()
	err := db.DB().QueryRowContext(context.Background(),
		"SELECT kind, deleted_at FROM sync_tombstone WHERE id = ?", id).Scan(&kind, &deletedAt)
	if err == nil {
		return kind, deletedAt, true
	}
	if isNoRows(err) {
		return "", 0, false
	}
	t.Fatal(err)
	return "", 0, false
}

func TestKnownHostRemoveWithOptOutWritesNoTombstone(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "u-a")
	if err := db.SetKnownHostSyncOptIn(ctx, "u-a", false); err != nil {
		t.Fatal(err)
	}
	seedKnownHost(t, db, "kh-off")
	row, found, err := db.KnownHostGet(ctx, "host-kh-off", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed known host: found=%v err=%v", found, err)
	}
	if err := db.KnownHostRemove(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, found := knownHostTombstone(t, db, row.ID); found {
		t.Fatal("opt-in 关闭时删除不得产生墓碑")
	}
}

func TestKnownHostRemoveDefaultsToTombstone(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "u-default")
	seedKnownHost(t, db, "kh-default")
	row, found, err := db.KnownHostGet(ctx, "host-kh-default", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed known host: found=%v err=%v", found, err)
	}
	if err := db.KnownHostRemove(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	kind, deletedAt, found := knownHostTombstone(t, db, row.ID)
	if !found || kind != SyncTombstoneKindKnownHost || deletedAt <= 0 {
		t.Fatalf("default opt-in tombstone = %q %d %v", kind, deletedAt, found)
	}
}

func TestKnownHostRemoveWithOptInWritesUserTombstone(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "u-a")
	if err := db.SetKnownHostSyncOptIn(ctx, "u-a", true); err != nil {
		t.Fatal(err)
	}
	seedKnownHost(t, db, "kh-on")
	row, found, err := db.KnownHostGet(ctx, "host-kh-on", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed known host: found=%v err=%v", found, err)
	}
	if err := db.KnownHostRemove(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	kind, deletedAt, found := knownHostTombstone(t, db, row.ID)
	if !found || kind != SyncTombstoneKindKnownHost || deletedAt <= 0 {
		t.Fatalf("tombstone = %q %d %v", kind, deletedAt, found)
	}
	// 用户删除墓碑不得携带 known_host 冲突三元组元数据(M163 冲突墓碑路径保持独立)
	var metas int
	if err := db.DB().QueryRowContext(context.Background(),
		"SELECT count(*) FROM setting WHERE key = ?", "sync.kh_tombstone."+row.ID).Scan(&metas); err != nil {
		t.Fatal(err)
	}
	if metas != 0 {
		t.Fatal("用户删除墓碑不得带冲突三元组元数据")
	}
}

func TestKnownHostRemoveWithoutIdentityWritesNoTombstone(t *testing.T) {
	db := testStore(t)
	// 即使某用户已开启 opt-in, 无身份的删除路径(桌面直连/匿名)也不立碑, 不得退化为全局设置
	if err := db.SetKnownHostSyncOptIn(context.Background(), "u-a", true); err != nil {
		t.Fatal(err)
	}
	seedKnownHost(t, db, "kh-anon")
	row, found, err := db.KnownHostGet(context.Background(), "host-kh-anon", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed known host: found=%v err=%v", found, err)
	}
	if err := db.KnownHostRemove(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, found := knownHostTombstone(t, db, row.ID); found {
		t.Fatal("无身份删除不得产生墓碑")
	}
}

func TestKnownHostRemoveOptInIsolatedPerUser(t *testing.T) {
	db := testStore(t)
	if err := db.SetKnownHostSyncOptIn(context.Background(), "u-a", true); err != nil {
		t.Fatal(err)
	}
	seedKnownHost(t, db, "kh-b")
	row, found, err := db.KnownHostGet(context.Background(), "host-kh-b", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed known host: found=%v err=%v", found, err)
	}
	// A 开 B 关: B 的删除走 B 自己的 opt-in(关), 不立碑
	ctxB := ipc.WithUserID(context.Background(), "u-b")
	if err := db.SetKnownHostSyncOptIn(ctxB, "u-b", false); err != nil {
		t.Fatal(err)
	}
	if err := db.KnownHostRemove(ctxB, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, found := knownHostTombstone(t, db, row.ID); found {
		t.Fatal("B 未开启 opt-in, 删除不得产生墓碑")
	}
}

func TestKnownHostSyncOptInPerUserRoundtrip(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if enabled, err := db.KnownHostSyncOptIn(ctx, "u-a"); err != nil || !enabled {
		t.Fatalf("default opt-in should be on: %v, %v", enabled, err)
	}
	if err := db.SetKnownHostSyncOptIn(ctx, "u-a", true); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := db.KnownHostSyncOptIn(ctx, "u-a"); !enabled {
		t.Fatal("u-a opt-in should be on")
	}
	if enabled, _ := db.KnownHostSyncOptIn(ctx, "u-b"); !enabled {
		t.Fatal("u-b 应保持默认开启")
	}
	if err := db.SetKnownHostSyncOptIn(ctx, "u-a", false); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := db.KnownHostSyncOptIn(ctx, "u-a"); enabled {
		t.Fatal("u-a opt-in should be off again")
	}
	if _, err := db.KnownHostSyncOptIn(ctx, ""); err == nil {
		t.Fatal("空 userID 必须拒绝, 防止退化为全局键")
	}
	if err := db.SetKnownHostSyncOptIn(ctx, "", true); err == nil {
		t.Fatal("空 userID 必须拒绝写入")
	}
}

func TestSyncTombstoneRecordKeepsMaxRevision(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if err := db.SyncTombstoneRecord(ctx, "kh-x", SyncTombstoneKindKnownHost, 200); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncTombstoneRecord(ctx, "kh-x", SyncTombstoneKindKnownHost, 100); err != nil {
		t.Fatal(err)
	}
	_, deletedAt, found := knownHostTombstone(t, db, "kh-x")
	if !found || deletedAt != 200 {
		t.Fatalf("tombstone revision = %d, want max 200", deletedAt)
	}
}

func TestAIProfilesDeleteTxAtomic(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	userCtx := ipc.WithUserID(ctx, "u-a")
	state := `{"version":1,"profiles":[],"activeId":null}`

	// opt-in 开: 状态与墓碑同一事务提交
	if err := db.SetAIProfileSyncOptIn(ctx, "u-a", true); err != nil {
		t.Fatal(err)
	}
	if err := db.AIProfilesDeleteTx(userCtx, state, "p-1", 100); err != nil {
		t.Fatal(err)
	}
	if raw, found, err := db.SettingGet(ctx, AIProfilesSettingKey); err != nil || !found || raw != state {
		t.Fatalf("ai.models 必须写回: found=%v err=%v", found, err)
	}
	if kind, deletedAt, found := knownHostTombstone(t, db, "p-1"); !found || kind != SyncTombstoneKindAIProfile || deletedAt != 100 {
		t.Fatalf("tombstone = %q %d %v", kind, deletedAt, found)
	}

	// opt-in 关: 只写状态不立碑
	if err := db.SetAIProfileSyncOptIn(ctx, "u-a", false); err != nil {
		t.Fatal(err)
	}
	if err := db.AIProfilesDeleteTx(userCtx, state, "p-2", 200); err != nil {
		t.Fatal(err)
	}
	if _, _, found := knownHostTombstone(t, db, "p-2"); found {
		t.Fatal("opt-in 关闭时不得立碑")
	}

	// 无身份: 只写状态不立碑
	if err := db.AIProfilesDeleteTx(ctx, state, "p-3", 300); err != nil {
		t.Fatal(err)
	}
	if _, _, found := knownHostTombstone(t, db, "p-3"); found {
		t.Fatal("无身份不得立碑")
	}
}

func TestAIProfilesDeleteTxDefaultsToTombstone(t *testing.T) {
	db := testStore(t)
	ctx := ipc.WithUserID(context.Background(), "u-default")
	state := `{"version":1,"profiles":[],"activeId":null}`
	if err := db.AIProfilesDeleteTx(ctx, state, "p-default", 100); err != nil {
		t.Fatal(err)
	}
	kind, deletedAt, found := knownHostTombstone(t, db, "p-default")
	if !found || kind != SyncTombstoneKindAIProfile || deletedAt != 100 {
		t.Fatalf("default opt-in tombstone = %q %d %v", kind, deletedAt, found)
	}
}
