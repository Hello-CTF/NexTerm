package store_test

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/dbtest"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestCommandLogInsertQueryFiltersPostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	now := int64(1700000000000)
	exit := 1
	for _, input := range []store.CommandLogInput{
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", UserID: "u-1", Command: "ls -la", Source: "terminal", ExitCode: &exit, StartedAt: now, FinishedAt: now + 100},
		{SessionID: "sess-1", TabID: "tab-2", AssetID: "asset-1", Command: "df -h", Source: "terminal", StartedAt: now + 200, FinishedAt: now + 250},
		{SessionID: "sess-2", TabID: "tab-3", AssetID: "asset-2", UserID: "u-2", Command: "ipconfig /all", Source: "exec", StartedAt: now + 300, FinishedAt: now + 350},
	} {
		if err := db.CommandLogInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	count, err := db.CommandLogCount(ctx, store.CommandLogQuery{})
	if err != nil || count != 3 {
		t.Fatalf("count without filters=%d err=%v", count, err)
	}
	count, err = db.CommandLogCount(ctx, store.CommandLogQuery{SessionID: auditPtr("sess-1"), AssetID: auditPtr("asset-1")})
	if err != nil || count != 2 {
		t.Fatalf("count by session+asset=%d err=%v", count, err)
	}
	count, err = db.CommandLogCount(ctx, store.CommandLogQuery{UserID: auditPtr("u-2")})
	if err != nil || count != 1 {
		t.Fatalf("count by user=%d err=%v", count, err)
	}

	rows, err := db.CommandLogQuery(ctx, store.CommandLogQuery{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("query without filters rows=%d err=%v", len(rows), err)
	}
	if rows[2].Command != "ls -la" || rows[2].ExitCode == nil || *rows[2].ExitCode != 1 {
		t.Fatalf("query order or exit code mismatch: %+v", rows[2])
	}
	if rows[1].UserID != nil {
		t.Fatalf("empty user id must scan as nil: %+v", rows[1])
	}
	rows, err = db.CommandLogQuery(ctx, store.CommandLogQuery{UserID: auditPtr("u-1"), Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].Command != "ls -la" {
		t.Fatalf("query by user rows=%+v err=%v", rows, err)
	}
}
