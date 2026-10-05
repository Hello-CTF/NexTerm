package production

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestAuditCountIPCFollowsFilters(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	assetID := "asset-1"
	for _, input := range []store.AuditInput{
		{Source: "user", Kind: "connect", Payload: map[string]any{"n": 1}},
		{Source: "user", Kind: "connect", Payload: map[string]any{"n": 2}, AssetID: &assetID},
		{Source: "user", Kind: "exec", Payload: map[string]any{"n": 3}},
		{Source: "ai", Kind: store.OutcomeAuditKind, Payload: map[string]any{"n": 4}},
		{Source: "ai", Kind: store.OutcomeAuditKind, Payload: map[string]any{"n": 5}},
	} {
		if err := database.AuditInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	count := func(args string) int64 {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "audit_count", `{"args":`+args+`}`)
		var result auditCountDTO
		requireStoreTestResponse(t, response, &result)
		return result.Total
	}
	for args, want := range map[string]int64{
		`{}`:                                   5,
		`{"source":"user"}`:                    3,
		`{"source":"ai"}`:                      2,
		`{"kind":"connect"}`:                   2,
		`{"source":"user","kind":"connect"}`:   2,
		`{"source":"user","kind":"exec"}`:      1,
		`{"source":"ai","kind":"connect"}`:     0,
		`{"assetId":"asset-1"}`:                1,
		`{"source":"ai","assetId":"asset-1"}`:  0,
		`{"source":"user","kind":"missing"}`:   0,
		`{"sessionId":"none","source":"user"}`: 0,
	} {
		if got := count(args); got != want {
			t.Fatalf("audit_count %s = %d, want %d", args, got, want)
		}
	}

	query := func(args string) []auditDTO {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "audit_query", `{"args":`+args+`}`)
		var rows []auditDTO
		requireStoreTestResponse(t, response, &rows)
		return rows
	}
	if got := query(`{"limit":2,"offset":0}`); len(got) != 2 {
		t.Fatalf("page 1 = %+v", got)
	}
	page2 := query(`{"limit":2,"offset":2}`)
	if len(page2) != 2 || page2[0].ID == query(`{"limit":2,"offset":0}`)[0].ID {
		t.Fatalf("page 2 = %+v", page2)
	}
	if got := query(`{"limit":2,"offset":4}`); len(got) != 1 {
		t.Fatalf("page 3 = %+v", got)
	}
	if got := query(`{"limit":2,"offset":5}`); len(got) != 0 {
		t.Fatalf("page 4 = %+v", got)
	}
	if got := query(`{"limit":2,"offset":0,"source":"user"}`); len(got) != 2 {
		t.Fatalf("filtered page = %+v", got)
	}
	if got := query(`{"limit":2,"offset":2,"source":"user"}`); len(got) != 1 {
		t.Fatalf("filtered last page = %+v", got)
	}
}
