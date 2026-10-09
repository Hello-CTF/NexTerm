package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// 对象类事务原子性: 资产类中途失败时整类回滚, 更早提交的对象类(分组)不受影响。
func TestImportBundleRollsBackFailedAssetClass(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)

	boom := store.AssetRow{ID: ids.New(), Kind: "ssh", Name: "boom-local", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1}
	putTestAsset(t, instance, boom)
	if _, err := instance.db.DB().ExecContext(ctx, `CREATE TRIGGER fail_boom_update BEFORE UPDATE ON asset
WHEN NEW.name = 'boom' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}

	groupID := ids.New()
	freshID := ids.New()
	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Groups:   []SyncBundleGroup{{ID: groupID, Name: "g", CreatedAt: 1, UpdatedAt: 1}},
		Assets: []SyncBundleAsset{
			{ID: freshID, Name: "fresh", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
			{ID: boom.ID, Name: "boom", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2},
		},
	}
	if _, err := instance.service.ImportBundle(ctx, ImportBundleRequest{Bundle: bundle}); err == nil {
		t.Fatal("import must fail when the asset class write fails")
	}
	if _, err := instance.db.AssetGet(ctx, freshID); !isNotFound(err) {
		t.Fatalf("asset class must roll back, fresh asset leaked: %v", err)
	}
	got, err := instance.db.AssetGet(ctx, boom.ID)
	if err != nil || got.Name != "boom-local" || got.UpdatedAt != 1 {
		t.Fatalf("existing asset must stay untouched: %+v err=%v", got, err)
	}
	if _, err := instance.db.GroupGet(ctx, groupID); err != nil {
		t.Fatalf("earlier group class must stay committed: %v", err)
	}
}
