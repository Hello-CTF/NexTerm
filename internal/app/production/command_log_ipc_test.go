package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestCommandLogIPCFollowsFilters(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}

	exit := 0
	for _, input := range []store.CommandLogInput{
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", UserID: "u-1", Command: "ls -la", Source: "terminal", ExitCode: &exit, StartedAt: 100, FinishedAt: 200},
		{SessionID: "sess-1", TabID: "tab-2", AssetID: "asset-1", Command: "df -h", Source: "terminal", StartedAt: 300, FinishedAt: 350},
		{SessionID: "sess-2", TabID: "tab-3", AssetID: "asset-2", UserID: "u-2", Command: "ipconfig /all", Source: "exec", ExitCode: &exit, StartedAt: 400, FinishedAt: 450},
	} {
		if err := database.CommandLogInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	count := func(args string) int64 {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "command_count", `{"args":`+args+`}`)
		var result auditCountDTO
		requireStoreTestResponse(t, response, &result)
		return result.Total
	}
	for args, want := range map[string]int64{
		`{}`:                                   3,
		`{"sessionId":"sess-1"}`:               2,
		`{"assetId":"asset-1"}`:                2,
		`{"userId":"u-1"}`:                     1,
		`{"userId":"u-2"}`:                     1,
		`{"sessionId":"sess-2"}`:               1,
		`{"sessionId":"missing"}`:              0,
		`{"assetId":"asset-1","userId":"u-2"}`: 0,
	} {
		if got := count(args); got != want {
			t.Fatalf("command_count %s = %d, want %d", args, got, want)
		}
	}

	query := func(args string) []commandLogDTO {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "command_query", `{"args":`+args+`}`)
		var rows []commandLogDTO
		requireStoreTestResponse(t, response, &rows)
		return rows
	}
	if got := query(`{"limit":2,"offset":0}`); len(got) != 2 || got[0].Command != "ipconfig /all" {
		t.Fatalf("page 1 = %+v", got)
	}
	if got := query(`{"limit":2,"offset":2}`); len(got) != 1 || got[0].Command != "ls -la" {
		t.Fatalf("page 2 = %+v", got)
	}
	if got := query(`{"sessionId":"sess-1","assetId":"asset-1"}`); len(got) != 2 {
		t.Fatalf("filtered = %+v", got)
	}
	if got := query(`{"userId":"u-1"}`); len(got) != 1 || got[0].UserID == nil || *got[0].UserID != "u-1" || got[0].ExitCode == nil || *got[0].ExitCode != 0 {
		t.Fatalf("by user = %+v", got)
	}
	if got := query(`{"userId":"missing"}`); len(got) != 0 {
		t.Fatalf("missing user = %+v", got)
	}
}

// 服务端装配(desktop=false)下, 已登录的非超管调用者无论是否自报 userId 都只能
// 读到自己的命令记录; 超管可全量查询; 无身份的开放部署保持原行为。
func TestCommandLogIPCServerModeScopesToCaller(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, t.TempDir(), false); err != nil {
		t.Fatal(err)
	}

	exit := 0
	for _, input := range []store.CommandLogInput{
		{SessionID: "sess-1", TabID: "tab-1", AssetID: "asset-1", UserID: "u-1", Command: "ls -la", Source: "terminal", ExitCode: &exit, StartedAt: 100, FinishedAt: 200},
		{SessionID: "sess-2", TabID: "tab-3", AssetID: "asset-2", UserID: "u-2", Command: "ipconfig /all", Source: "exec", ExitCode: &exit, StartedAt: 400, FinishedAt: 450},
		{SessionID: "sess-3", TabID: "tab-4", AssetID: "asset-2", UserID: "u-2", Command: "whoami", Source: "terminal", ExitCode: &exit, StartedAt: 500, FinishedAt: 550},
	} {
		if err := database.CommandLogInsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	dispatch := func(ctx context.Context, command, args string) ipc.Response {
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	count := func(ctx context.Context, args string) int64 {
		t.Helper()
		response := dispatch(ctx, "command_count", `{"args":`+args+`}`)
		var result auditCountDTO
		requireStoreTestResponse(t, response, &result)
		return result.Total
	}
	query := func(ctx context.Context, args string) []commandLogDTO {
		t.Helper()
		response := dispatch(ctx, "command_query", `{"args":`+args+`}`)
		var rows []commandLogDTO
		requireStoreTestResponse(t, response, &rows)
		return rows
	}

	plain := ipc.WithUserID(ctx, "u-1")
	plain = ipc.WithRole(plain, "user")
	admin := ipc.WithUserID(ctx, "u-admin")
	admin = ipc.WithRole(admin, "superadmin")

	if got := count(plain, `{}`); got != 1 {
		t.Fatalf("non-admin unfiltered count = %d, want 1", got)
	}
	if got := query(plain, `{}`); len(got) != 1 || got[0].UserID == nil || *got[0].UserID != "u-1" {
		t.Fatalf("non-admin unfiltered query = %+v", got)
	}
	// 自报他人 userId 被强制收敛为调用者自己: 读到的是 u-1 的记录而不是 u-2 的。
	if got := count(plain, `{"userId":"u-2"}`); got != 1 {
		t.Fatalf("non-admin cross-user count = %d, want forced-to-self 1", got)
	}
	if got := query(plain, `{"userId":"u-2"}`); len(got) != 1 || got[0].UserID == nil || *got[0].UserID != "u-1" {
		t.Fatalf("non-admin cross-user query = %+v", got)
	}
	if got := count(admin, `{}`); got != 3 {
		t.Fatalf("superadmin unfiltered count = %d, want 3", got)
	}
	if got := query(admin, `{"userId":"u-2"}`); len(got) != 2 || got[0].Command != "whoami" {
		t.Fatalf("superadmin filtered query = %+v", got)
	}
	if got := count(ctx, `{}`); got != 3 {
		t.Fatalf("identity-less (open deployment) count = %d, want 3", got)
	}
	if got := query(ctx, `{"userId":"u-1"}`); len(got) != 1 {
		t.Fatalf("identity-less (open deployment) query = %+v", got)
	}
}
