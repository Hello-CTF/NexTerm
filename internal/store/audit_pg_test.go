package store_test

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/dbtest"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestAuditCountAndQueryOptionalFilterTypesPostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	inputs := []store.AuditInput{
		{Source: "user", Kind: "connect", SessionID: auditPtr("sess-1"), AssetID: auditPtr("asset-1"), Payload: map[string]any{"n": 1}},
		{Source: "user", Kind: "exec", SessionID: auditPtr("sess-1"), Payload: map[string]any{"n": 2}},
		{Source: "ai", Kind: "exec", Payload: map[string]any{"n": 3}},
	}
	for _, input := range inputs {
		if err := db.AuditInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	count, err := db.AuditCount(ctx, store.AuditQuery{})
	if err != nil || count != 3 {
		t.Fatalf("count without filters=%d err=%v", count, err)
	}
	count, err = db.AuditCount(ctx, store.AuditQuery{Source: auditPtr("user")})
	if err != nil || count != 2 {
		t.Fatalf("count by source=%d err=%v", count, err)
	}
	count, err = db.AuditCount(ctx, store.AuditQuery{Source: auditPtr("user"), Kind: auditPtr("connect")})
	if err != nil || count != 1 {
		t.Fatalf("count by source+kind=%d err=%v", count, err)
	}

	rows, err := db.AuditQuery(ctx, store.AuditQuery{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("query without filters rows=%d err=%v", len(rows), err)
	}
	if rows[0].Source != "ai" || rows[2].Kind != "connect" {
		t.Fatalf("query order not ts DESC, id DESC: %+v", rows)
	}

	rows, err = db.AuditQuery(ctx, store.AuditQuery{Kind: auditPtr("exec"), Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].Source != "ai" {
		t.Fatalf("query by kind rows=%+v err=%v", rows, err)
	}
	rows, err = db.AuditQuery(ctx, store.AuditQuery{SessionID: auditPtr("sess-1"), AssetID: auditPtr("asset-1")})
	if err != nil || len(rows) != 1 || rows[0].Kind != "connect" {
		t.Fatalf("query by session+asset rows=%+v err=%v", rows, err)
	}
}

func auditPtr[T any](value T) *T { return &value }
