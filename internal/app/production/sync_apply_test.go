package production

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// Offline shared workspace boundary: the command layer does not ask for a session identity (an anonymous context can apply),
// and session plus CSRF are enforced by the /rpc transport layer (internal/server requireAuth) in account mode.
func TestSyncApplyObjectsCommandOfflineAnonymous(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, database)
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata("test", false))

	dispatcher := ipc.NewDispatcher()
	if err := syncService.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	if err := registerSyncApplyCommands(dispatcher, syncService); err != nil {
		t.Fatal(err)
	}
	if err := registerSyncApplyCommands(dispatcher, syncService); err == nil {
		t.Fatal("duplicate sync_apply_objects registration must fail")
	}

	groupID := ids.New()
	payload, err := json.Marshal(map[string]any{
		"id": groupID, "name": "浏览器分组", "sort": 0, "createdAt": 1, "updatedAt": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{
		"objects": []map[string]any{{"id": groupID, "kind": "group", "payload": json.RawMessage(payload)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]any{"args": json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{
		Command: syncservice.CommandApplyObjects, Args: envelope,
	}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("anonymous offline apply rejected: %+v", response.Error)
	}
	var result syncservice.ApplyObjectsResult
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Applied != 1 || result.Skipped != 0 || len(result.Objects) != 1 {
		t.Fatalf("result=%+v", result)
	}
	group, err := database.GroupGet(ctx, groupID)
	if err != nil || group.Name != "浏览器分组" {
		t.Fatalf("group=%+v err=%v", group, err)
	}
}

func TestSyncApplyObjectsCommandValidation(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	syncService := syncservice.New(database, vault.Load(ctx, database), syncservice.WithMetadata("test", false))
	dispatcher := ipc.NewDispatcher()
	if err := registerSyncApplyCommands(dispatcher, syncService); err != nil {
		t.Fatal(err)
	}

	dispatch := func(args string) ipc.Response {
		return dispatcher.Dispatch(context.Background(), ipc.Request{
			Command: syncservice.CommandApplyObjects, Args: json.RawMessage(args),
		}, ipc.Environment{})
	}

	response := dispatch(`{"args":{"objects":[{"id":"bad-id","kind":"group","payload":{"id":"bad-id","name":"x","updatedAt":1}}]}}`)
	if !response.OK {
		t.Fatalf("per-object rejection must not fail the call: %+v", response.Error)
	}
	var result syncservice.ApplyObjectsResult
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Objects[0].Warning == "" {
		t.Fatalf("result=%+v", result)
	}

	response = dispatch(`{"args":{"objects":`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("malformed envelope = %+v, want bad_param", response)
	}

	response = dispatch(`{"objects":[]}`)
	if !response.OK {
		t.Fatalf("flat args decode = %+v", response.Error)
	}
}
