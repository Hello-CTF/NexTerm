package production

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func requireInventoryConfirmationDetail(t *testing.T, response ipc.Response) dockerConfirmationRequiredDetail {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("response = %+v, want needs_confirm", response)
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail dockerConfirmationRequiredDetail
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

func TestInventoryTargetExecProductionConfirmationUsesConfiguredEnvironmentNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager := newInventorySessionManager(t)
	executor := &fakeInventoryDockerExecutor{result: docker.ExecResult{Output: "Linux\n"}}
	service := newInventoryService(database, nil, false, t.TempDir(),
		withInventoryWatchInterval(20*time.Millisecond), withInventoryDockerAccess(manager, executor))
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	runner := &recordingInventoryRunner{outputs: map[string][]byte{}}
	service.runner = runner
	dispatcher := newInventoryExecDispatcher(t, service)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if err := guard.SetProductionEnvironments(ctx, database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "live-a", Host: stringPointerForInventory("prod.example"), Username: optionalString("ops"), Tags: "env:live",
	})
	if err != nil {
		t.Fatal(err)
	}
	connected := connectInventoryTestSession(t, manager, "owner-a", asset)
	importDockerContext(t, dispatcher, ctx, runner)

	request := inventoryDockerExecRequest()
	request.SessionID = connected.ID
	response := dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, nil)
	detail := requireInventoryConfirmationDetail(t, response)
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "live" {
		t.Fatalf("required environment names = %v", detail.RequiredEnvironmentNames)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("unconfirmed exec reached the docker backend: %+v", executor.calls)
	}

	request.Confirmation = &dockerProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: []string{"production"},
	}
	response = dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, nil)
	requireInventoryIPCError(t, response, ipc.CodeNeedsConfirm)
	if len(executor.calls) != 0 {
		t.Fatalf("wrong-name exec reached the docker backend: %+v", executor.calls)
	}

	request.Confirmation.EnvironmentNames = []string{"live"}
	var result inventoryTargetExecResponse
	dispatchInventoryTest(t, dispatcher, ctx, "inventory_target_exec", request, &result)
	if result.Output != "Linux\n" {
		t.Fatalf("exec output = %q", result.Output)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("docker backend calls = %+v", executor.calls)
	}
	rows, err := database.AuditQuery(ctx, store.AuditQuery{Limit: 10})
	if err != nil || len(rows) != 2 || rows[0].Kind != "inventory_target_exec" || rows[1].Kind != "docker_production_confirmation" {
		t.Fatalf("confirmation audit rows = %+v, err=%v", rows, err)
	}
}
