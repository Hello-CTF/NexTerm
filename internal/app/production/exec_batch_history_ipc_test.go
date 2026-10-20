package production

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestExecBatchHistoryIPCOwnerIsolationAndMetadataOnly(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	finishedAt := int64(2000)
	for _, item := range []struct {
		ctx     context.Context
		session string
	}{
		{ctxA, "session-self"},
		{context.Background(), "session-ownerless"},
	} {
		if err := database.ExecBatchHistorySave(item.ctx, store.ExecBatchRow{
			ID: "shared-id", CommandHash: strings.Repeat("a", 64), TargetSetHash: strings.Repeat("b", 64),
			Status: store.ExecBatchStatusSuccess, TargetCount: 1, StartedAt: 1000, FinishedAt: &finishedAt,
		}, []store.ExecBatchResultRow{{
			SessionID: item.session, SessionName: "target", Position: 0,
			Status: "success", StartedAt: &finishedAt, FinishedAt: &finishedAt,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	service := newExecBatchIPCService(nil, nil, nil, database)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	response := dispatchExecBatchTest(dispatcher, ctxA, "exec_batch_history_list", store.ExecBatchHistoryQuery{Limit: 10})
	page := decodeExecBatchTest[store.ExecBatchHistoryPage](t, response)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "shared-id" || page.Items[0].OwnerScope != store.ExecBatchHistoryScopeSelf {
		t.Fatalf("owner A history page = %+v", page)
	}
	response = dispatchExecBatchTest(dispatcher, ctxB, "exec_batch_history_list", store.ExecBatchHistoryQuery{})
	page = decodeExecBatchTest[store.ExecBatchHistoryPage](t, response)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("owner B history page = %+v", page)
	}
	adminA := ipc.WithRole(ctxA, "superadmin")
	response = dispatchExecBatchTest(dispatcher, adminA, "exec_batch_history_list", store.ExecBatchHistoryQuery{})
	page = decodeExecBatchTest[store.ExecBatchHistoryPage](t, response)
	if page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "shared-id" || page.Items[1].ID != "shared-id" ||
		page.Items[0].OwnerScope == page.Items[1].OwnerScope {
		t.Fatalf("superadmin same-id history page = %+v", page)
	}
	response = dispatchExecBatchTest(dispatcher, adminA, "exec_batch_history_get", execBatchHistoryGetRequest{
		BatchID: "shared-id", Scope: store.ExecBatchHistoryScopeSelf,
	})
	history := decodeExecBatchTest[store.ExecBatchHistory](t, response)
	if history.Batch.ID != "shared-id" || len(history.Results) != 1 || history.Results[0].SessionID != "session-self" {
		t.Fatalf("superadmin self history = %+v", history)
	}
	for _, forbidden := range []string{"stdout", "stderr", "command-secret", "raw-error"} {
		if strings.Contains(string(response.Data), forbidden) {
			t.Fatalf("history IPC contains %q: %s", forbidden, response.Data)
		}
	}
	response = dispatchExecBatchTest(dispatcher, adminA, "exec_batch_history_get", execBatchHistoryGetRequest{
		BatchID: "shared-id", Scope: store.ExecBatchHistoryScopeOwnerless,
	})
	history = decodeExecBatchTest[store.ExecBatchHistory](t, response)
	if len(history.Results) != 1 || history.Results[0].SessionID != "session-ownerless" {
		t.Fatalf("superadmin ownerless history = %+v", history)
	}
	response = dispatchExecBatchTest(dispatcher, ctxB, "exec_batch_history_get", execBatchHistoryGetRequest{
		BatchID: "shared-id", Scope: store.ExecBatchHistoryScopeOwnerless,
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("regular ownerless-scope history get = %+v", response)
	}
	response = dispatchExecBatchTest(dispatcher, adminA, "exec_batch_history_get", execBatchHistoryGetRequest{
		BatchID: "shared-id", Scope: "owner-b",
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("arbitrary owner scope history get = %+v", response)
	}
}
