package store

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestAuditCountAndQueryOptionalFilters(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)

	inputs := []AuditInput{
		{Source: "user", Kind: "connect", SessionID: ptr("sess-1"), AssetID: ptr("asset-1"), Payload: map[string]any{"n": 1}},
		{Source: "user", Kind: "exec", SessionID: ptr("sess-1"), Payload: map[string]any{"n": 2}},
		{Source: "ai", Kind: "exec", Payload: map[string]any{"n": 3}},
	}
	for _, input := range inputs {
		if err := db.AuditInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	for _, testCase := range []struct {
		name  string
		query AuditQuery
		want  int64
	}{
		{"no filters", AuditQuery{}, 3},
		{"source", AuditQuery{Source: ptr("user")}, 2},
		{"kind", AuditQuery{Kind: ptr("exec")}, 2},
		{"source and kind", AuditQuery{Source: ptr("user"), Kind: ptr("connect")}, 1},
		{"session and asset", AuditQuery{SessionID: ptr("sess-1"), AssetID: ptr("asset-1")}, 1},
		{"unmatched source", AuditQuery{Source: ptr("missing")}, 0},
		{"session and kind", AuditQuery{SessionID: ptr("missing"), Kind: ptr("exec")}, 0},
	} {
		count, err := db.AuditCount(ctx, testCase.query)
		if err != nil || count != testCase.want {
			t.Fatalf("%s: count=%d want=%d err=%v", testCase.name, count, testCase.want, err)
		}
	}

	rows, err := db.AuditQuery(ctx, AuditQuery{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("query without filters rows=%d err=%v", len(rows), err)
	}
	if rows[0].Source != "ai" || rows[1].Kind != "exec" || rows[2].Kind != "connect" {
		t.Fatalf("query order not ts DESC, id DESC: %+v", rows)
	}
	if rows[0].SessionID != nil || rows[0].AssetID != nil {
		t.Fatalf("nil-able columns must scan as nil: %+v", rows[0])
	}

	rows, err = db.AuditQuery(ctx, AuditQuery{Source: ptr("user"), Kind: ptr("exec")})
	if err != nil || len(rows) != 1 || rows[0].SessionID == nil || *rows[0].SessionID != "sess-1" || rows[0].AssetID != nil {
		t.Fatalf("query by source+kind rows=%+v err=%v", rows, err)
	}

	rows, err = db.AuditQuery(ctx, AuditQuery{AssetID: ptr("asset-1")})
	if err != nil || len(rows) != 1 || rows[0].Kind != "connect" {
		t.Fatalf("query by asset rows=%+v err=%v", rows, err)
	}

	firstPage, err := db.AuditQuery(ctx, AuditQuery{Limit: 2})
	if err != nil || len(firstPage) != 2 || firstPage[0].Source != "ai" {
		t.Fatalf("first page rows=%+v err=%v", firstPage, err)
	}
	secondPage, err := db.AuditQuery(ctx, AuditQuery{Limit: 2, Offset: 2})
	if err != nil || len(secondPage) != 1 || secondPage[0].Kind != "connect" {
		t.Fatalf("second page rows=%+v err=%v", secondPage, err)
	}
}

func TestAuditUnownedAuthRowsAreVisibleOnlyToSuperadminAuthQuery(t *testing.T) {
	base := context.Background()
	db := testStore(t)
	admin := ipc.WithRole(ipc.WithUserID(base, "admin"), "superadmin")
	member := ipc.WithRole(ipc.WithUserID(base, "member"), "member")
	for _, row := range []struct {
		ctx    context.Context
		source string
	}{
		{ipc.WithUserID(base, "admin"), "auth"},
		{ipc.WithUserID(base, "member"), "auth"},
		{base, "auth"},
		{base, "user"},
	} {
		if err := db.AuditInsert(row.ctx, AuditInput{Source: row.source, Kind: "login_failure", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(ctx context.Context, source *string) int64 {
		n, err := db.AuditCount(ctx, AuditQuery{Source: source})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.AuditQuery(ctx, AuditQuery{Source: source})
		if err != nil || int64(len(rows)) != n {
			t.Fatalf("query rows=%d count=%d err=%v", len(rows), n, err)
		}
		return n
	}
	if got := count(admin, ptr("auth")); got != 2 {
		t.Fatalf("superadmin auth view = %d, want own + unowned", got)
	}
	if got := count(admin, nil); got != 1 {
		t.Fatalf("superadmin unfiltered view = %d, want own only", got)
	}
	if got := count(member, ptr("auth")); got != 1 {
		t.Fatalf("member auth view = %d, want own only", got)
	}
}
