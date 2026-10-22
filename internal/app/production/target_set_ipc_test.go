package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func newTargetSetIPCTestDispatcher(t *testing.T) (*ipc.Dispatcher, *store.Store) {
	t.Helper()
	database := dbtest.NewFixture(t).OpenStore(t)
	dispatcher := ipc.NewDispatcher()
	if err := registerTargetSetCommands(dispatcher, execbatch.NewTargetSetService(database)); err != nil {
		t.Fatal(err)
	}
	return dispatcher, database
}

func requireTargetSetIPCCode(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want code %s", response, code)
	}
}

func TestTargetSetIPCCommandsOwnerIsolationAndCRUD(t *testing.T) {
	dispatcher, _ := newTargetSetIPCTestDispatcher(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")

	// The payload cannot choose the owner: an injected ownerId is ignored
	// and the trusted context owner wins.
	created := decodeExecBatchTest[store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxA, "target_set_create", map[string]any{
		"name": "  web fleet  ", "sessionIds": []string{"s1", "s2"}, "ownerId": "owner-b",
	}))
	if created.ID == "" || created.Name != "web fleet" || created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Fatalf("created = %+v", created)
	}
	if len(created.SessionIDs) != 2 || created.SessionIDs[0] != "s1" || created.SessionIDs[1] != "s2" {
		t.Fatalf("created sessions = %+v", created.SessionIDs)
	}

	listA := decodeExecBatchTest[[]store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxA, "target_set_list", nil))
	if len(listA) != 1 || listA[0].ID != created.ID {
		t.Fatalf("owner A list = %+v", listA)
	}
	listB := decodeExecBatchTest[[]store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxB, "target_set_list", nil))
	if len(listB) != 0 {
		t.Fatalf("owner B sees owner A sets: %+v", listB)
	}

	response := dispatchExecBatchTest(dispatcher, ctxB, "target_set_get", map[string]any{"id": created.ID})
	requireTargetSetIPCCode(t, response, ipc.CodeNotFound)
	response = dispatchExecBatchTest(dispatcher, ctxB, "target_set_update", map[string]any{"id": created.ID, "name": "hijack", "sessionIds": []string{"x"}})
	requireTargetSetIPCCode(t, response, ipc.CodeNotFound)
	response = dispatchExecBatchTest(dispatcher, ctxB, "target_set_delete", map[string]any{"id": created.ID})
	requireTargetSetIPCCode(t, response, ipc.CodeNotFound)

	got := decodeExecBatchTest[store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxA, "target_set_get", map[string]any{"id": created.ID}))
	if got.Name != "web fleet" || len(got.SessionIDs) != 2 {
		t.Fatalf("cross-owner write touched the row: %+v", got)
	}

	updated := dispatchExecBatchTest(dispatcher, ctxA, "target_set_update", map[string]any{"id": created.ID, "name": "db fleet", "sessionIds": []string{"s9"}})
	if !updated.OK {
		t.Fatalf("update = %+v", updated)
	}
	got = decodeExecBatchTest[store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxA, "target_set_get", map[string]any{"id": created.ID}))
	if got.Name != "db fleet" || len(got.SessionIDs) != 1 || got.SessionIDs[0] != "s9" {
		t.Fatalf("updated = %+v", got)
	}
	if got.UpdatedAt < created.UpdatedAt {
		t.Fatalf("update moved updated_at backwards: %+v", got)
	}

	deleted := dispatchExecBatchTest(dispatcher, ctxA, "target_set_delete", map[string]any{"id": created.ID})
	if !deleted.OK {
		t.Fatalf("delete = %+v", deleted)
	}
	response = dispatchExecBatchTest(dispatcher, ctxA, "target_set_get", map[string]any{"id": created.ID})
	requireTargetSetIPCCode(t, response, ipc.CodeNotFound)
	listA = decodeExecBatchTest[[]store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxA, "target_set_list", nil))
	if len(listA) != 0 {
		t.Fatalf("list after delete = %+v", listA)
	}
}

func TestTargetSetIPCCommandsValidationCodes(t *testing.T) {
	dispatcher, _ := newTargetSetIPCTestDispatcher(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")

	response := dispatchExecBatchTest(dispatcher, ctx, "target_set_create", map[string]any{"name": "   ", "sessionIds": []string{"s1"}})
	requireTargetSetIPCCode(t, response, ipc.CodeBadParam)
	response = dispatchExecBatchTest(dispatcher, ctx, "target_set_create", map[string]any{"name": "fleet", "sessionIds": []string{"s1", "s1"}})
	requireTargetSetIPCCode(t, response, ipc.CodeBadParam)
	response = dispatchExecBatchTest(dispatcher, ctx, "target_set_create", map[string]any{"name": "fleet", "sessionIds": []string{}})
	requireTargetSetIPCCode(t, response, ipc.CodeBadParam)
	response = dispatchExecBatchTest(dispatcher, ctx, "target_set_get", map[string]any{"id": " lead"})
	requireTargetSetIPCCode(t, response, ipc.CodeBadParam)
	response = dispatchExecBatchTest(dispatcher, ctx, "target_set_get", map[string]any{"id": "missing"})
	requireTargetSetIPCCode(t, response, ipc.CodeNotFound)
	response = dispatchExecBatchTest(dispatcher, ctx, "target_set_delete", map[string]any{"id": "missing"})
	requireTargetSetIPCCode(t, response, ipc.CodeNotFound)
}

func TestTargetSetIPCCommandsOwnerlessNamespace(t *testing.T) {
	dispatcher, _ := newTargetSetIPCTestDispatcher(t)
	ownerless := context.Background()

	created := decodeExecBatchTest[store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ownerless, "target_set_create", map[string]any{
		"name": "local", "sessionIds": []string{"s1"},
	}))
	list := decodeExecBatchTest[[]store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ownerless, "target_set_list", nil))
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("ownerless list = %+v", list)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	listA := decodeExecBatchTest[[]store.ExecTargetSet](t, dispatchExecBatchTest(dispatcher, ctxA, "target_set_list", nil))
	if len(listA) != 0 {
		t.Fatalf("owner A sees ownerless sets: %+v", listA)
	}
}
