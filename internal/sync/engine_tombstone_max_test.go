package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// 墓碑写入取大语义回归: 同 ID 较旧删除时间不得覆盖较新墓碑(SQLite 标量 max 路径)。
func TestSyncTombstonePutKeepsMaxDeletedAt(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	engine := instance.service.engine
	id := ids.New()

	if err := engine.syncTombstonePut(ctx, id, KindGroup, 100); err != nil {
		t.Fatal(err)
	}
	if err := engine.syncTombstonePut(ctx, id, KindGroup, 50); err != nil {
		t.Fatal(err)
	}
	row, found, err := engine.syncTombstoneGet(ctx, id)
	if err != nil || !found || row.Kind != KindGroup || row.DeletedAt != 100 {
		t.Fatalf("older tombstone overwrote newer: %+v found=%v err=%v", row, found, err)
	}
	if err := engine.syncTombstonePut(ctx, id, KindGroup, 200); err != nil {
		t.Fatal(err)
	}
	row, found, err = engine.syncTombstoneGet(ctx, id)
	if err != nil || !found || row.DeletedAt != 200 {
		t.Fatalf("newer tombstone must win: %+v found=%v err=%v", row, found, err)
	}
}
