package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func auditTimelineTestSetup(t *testing.T, desktop bool) (*ipc.Dispatcher, *store.Store) {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, "", desktop); err != nil {
		t.Fatal(err)
	}
	return dispatcher, database
}

func auditTimelineDispatch(dispatcher *ipc.Dispatcher, ctx context.Context, args string) ipc.Response {
	return dispatcher.Dispatch(ctx, ipc.Request{Command: "audit_timeline", Args: json.RawMessage(args)}, ipc.Environment{})
}

func seedAuditTimeline(t *testing.T, database *store.Store) {
	t.Helper()
	ctx := t.Context()
	for i, owner := range []string{"owner-a", "owner-b"} {
		audit := store.AuditRow{TS: int64(100 + i), Source: "user", Kind: "x", PayloadJSON: `{"k":1}`, OwnerID: owner}
		if err := query.Use(database.GORM()).AuditRow.WithContext(ctx).Create(&audit); err != nil {
			t.Fatal(err)
		}
		user := owner
		command := store.CommandLogRow{SessionID: "s", TabID: "t", AssetID: "h", UserID: &user, Command: "ls", Source: "terminal", StartedAt: int64(200 + i), FinishedAt: int64(210 + i)}
		if err := query.Use(database.GORM()).CommandLog.WithContext(ctx).Create(&command); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditTimelineDesktopIgnoresUserScope(t *testing.T) {
	dispatcher, database := auditTimelineTestSetup(t, true)
	seedAuditTimeline(t, database)
	var result auditTimelineResult
	requireStoreTestResponse(t, auditTimelineDispatch(dispatcher, ipc.WithUserID(t.Context(), "owner-a"), `{}`), &result)
	if len(result.Items) != 4 || result.NextCursor != nil {
		t.Fatalf("desktop items = %d next=%v, want all 4", len(result.Items), result.NextCursor)
	}
	var command *auditTimelineItemDTO
	for i := range result.Items {
		if result.Items[i].Stream == "command" {
			command = &result.Items[i]
		}
	}
	if command == nil || command.Command == nil || *command.Command != "ls" || command.DurationMS == nil || *command.DurationMS != 10 || command.Kind != "command" {
		t.Fatalf("command item = %+v", command)
	}
}

func TestAuditTimelineServerIsolatesUser(t *testing.T) {
	dispatcher, database := auditTimelineTestSetup(t, false)
	seedAuditTimeline(t, database)
	var result auditTimelineResult
	requireStoreTestResponse(t, auditTimelineDispatch(dispatcher, ipc.WithUserID(t.Context(), "owner-a"), `{}`), &result)
	if len(result.Items) != 2 {
		t.Fatalf("server items = %d, want 2 for owner-a", len(result.Items))
	}
	requireStoreTestResponse(t, auditTimelineDispatch(dispatcher, t.Context(), `{"limit":1}`), &result)
	if len(result.Items) != 1 || result.NextCursor == nil {
		t.Fatalf("ownerless paging items=%d next=%v", len(result.Items), result.NextCursor)
	}
}

func TestAuditTimelineEmptyAndBadCursor(t *testing.T) {
	dispatcher, _ := auditTimelineTestSetup(t, true)
	response := auditTimelineDispatch(dispatcher, t.Context(), `{}`)
	if !response.OK {
		t.Fatalf("empty timeline failed: %+v", response)
	}
	if !strings.Contains(string(response.Data), `"items":[]`) {
		t.Fatalf("empty items must serialize as [], got %s", response.Data)
	}
	response = auditTimelineDispatch(dispatcher, t.Context(), `{"cursor":{"ts":1,"stream":"nope","id":1}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("bad cursor response = %+v, want bad param", response)
	}
}
