package production

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestStoreCommandsProjectDTOsAndPreserveTriStatePatches(t *testing.T) {
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
	for _, command := range []string{
		"layout_get", "layout_put", "asset_list", "asset_get", "asset_create", "asset_update", "asset_delete", "asset_search",
		"asset_save_key_file", "asset_read_key_file", "group_list", "group_create", "group_update", "group_delete",
		"snippet_list", "snippet_create", "snippet_update", "snippet_delete", "audit_query", "audit_count", "known_host_list", "known_host_accept", "known_host_remove",
	} {
		if !slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("missing %s in %v", command, dispatcher.Commands())
		}
	}

	response := dispatchStoreTest(dispatcher, "asset_create", `{"args":{"kind":"ssh","name":" Production ","host":"127.0.0.1","port":22,"username":"root","authKind":"password","options":{"encoding":"utf-8"}}}`)
	var created assetDTO
	requireStoreTestResponse(t, response, &created)
	if created.Name != "Production" || string(created.Options) != `{"encoding":"utf-8"}` || created.Host == nil || *created.Host != "127.0.0.1" {
		t.Fatalf("created asset = %+v", created)
	}
	response = dispatchStoreTest(dispatcher, "asset_update", `{"args":{"id":"`+created.ID+`","host":null,"options":{"encoding":"gbk"}}}`)
	var updated assetDTO
	requireStoreTestResponse(t, response, &updated)
	if updated.Name != "Production" || updated.Host != nil || string(updated.Options) != `{"encoding":"gbk"}` {
		t.Fatalf("null patch = %+v", updated)
	}
	response = dispatchStoreTest(dispatcher, "asset_update", `{"args":{"id":"`+created.ID+`","note":"kept host null"}}`)
	requireStoreTestResponse(t, response, &updated)
	if updated.Host != nil || string(updated.Options) != `{"encoding":"gbk"}` {
		t.Fatalf("omitted patch changed fields = %+v", updated)
	}

	response = dispatchStoreTest(dispatcher, "layout_put", `{"args":{"data":"{\"workspaces\":[]}","revision":0}}`)
	var saved layoutSaveDTO
	requireStoreTestResponse(t, response, &saved)
	if !saved.Saved || saved.Revision != 1 || saved.Conflict {
		t.Fatalf("layout save = %+v", saved)
	}
	response = dispatchStoreTest(dispatcher, "layout_put", `{"args":{"data":"{}","revision":0}}`)
	requireStoreTestResponse(t, response, &saved)
	if saved.Saved || saved.Revision != 1 || !saved.Conflict {
		t.Fatalf("layout conflict = %+v", saved)
	}

	if err := database.AuditInsert(ctx, store.AuditInput{Source: "user", Kind: "connect", Payload: map[string]any{"asset": created.Name}}); err != nil {
		t.Fatal(err)
	}
	response = dispatchStoreTest(dispatcher, "audit_query", `{"args":{"kind":"connect"}}`)
	var audits []auditDTO
	requireStoreTestResponse(t, response, &audits)
	if len(audits) != 1 || string(audits[0].Payload) != `{"asset":"Production"}` {
		t.Fatalf("audit DTOs = %+v", audits)
	}

	response = dispatchStoreTest(dispatcher, "group_create", `{"name":"parent"}`)
	var group groupDTO
	requireStoreTestResponse(t, response, &group)
	response = dispatchStoreTest(dispatcher, "group_update", `{"id":"`+group.ID+`","parentId":null}`)
	requireStoreTestResponse(t, response, &group)
	if group.ParentID != nil {
		t.Fatalf("group parent = %+v", group.ParentID)
	}

	response = dispatchStoreTest(dispatcher, "asset_save_key_file", `{"content":"PRIVATE KEY"}`)
	var savedFile map[string]string
	requireStoreTestResponse(t, response, &savedFile)
	response = dispatchStoreTest(dispatcher, "asset_read_key_file", `{"path":"`+savedFile["path"]+`"}`)
	var content string
	requireStoreTestResponse(t, response, &content)
	if content != "PRIVATE KEY" {
		t.Fatalf("key content = %q", content)
	}
	info, err := os.Stat(savedFile["path"])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
}

func dispatchStoreTest(dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func requireStoreTestResponse(t *testing.T, response ipc.Response, target any) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response)
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		t.Fatalf("decode %s: %v", response.Data, err)
	}
}
