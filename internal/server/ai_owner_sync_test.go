package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

func newAIOwnerRPCFixture(t *testing.T) (*accountFixture, *store.Store, *syncservice.Service) {
	t.Helper()
	database, _, syncService := newRealSyncService(t)
	dispatcher := ipc.NewDispatcher()
	if err := syncService.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	if err := ipc.RegisterNested(dispatcher, syncservice.CommandApplyObjects, func(ctx context.Context, _ *ipc.Call, input syncservice.ApplyObjectsRequest) (syncservice.ApplyObjectsResult, error) {
		return syncService.ApplyObjects(ctx, input)
	}); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Accounts = account.New(database.GORM())
	config.Dispatcher = dispatcher
	config.GatewayAuthKey = "gateway-secret"
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: config.Accounts, db: database.DB(), client: httpServer.Client(),
	}, database, syncService
}

func seedLegacyAIState(t *testing.T, database *store.Store) {
	t.Helper()
	ctx := context.Background()
	for key, value := range map[string]string{
		store.AIProfilesSettingKey:       `{"version":1,"profiles":[{"id":"legacy","name":"legacy","apiKey":"legacy-secret","model":"m1"}],"activeId":"legacy"}`,
		guard.PermissionSettingKey:       `{"mode":"silent","dangerRules":[]}`,
		guard.DeviceGrantsSettingKey:     `{"legacy":{"deviceId":"legacy","kinds":["terminal_write"],"updatedAt":1}}`,
		guard.GrantRulesSettingKey:       `[]`,
		guard.DevicePermissionSettingKey: `{"legacy":"read_only"}`,
	} {
		if err := database.SettingSet(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB().ExecContext(ctx, `INSERT INTO sync_tombstone(owner_id,id,kind,deleted_at) VALUES
		('', 'global', 'ai_permission', 100),('', 'group-legacy', 'group', 100)`); err != nil {
		t.Fatal(err)
	}
}

func aiRPC(t *testing.T, fixture *accountFixture, session *accountTestSession, command string, args any, gateway bool) accountCall {
	headers := map[string]string{}
	if gateway {
		headers[GatewayAuthHeader] = "gateway-secret"
	}
	csrf := ""
	if session != nil {
		csrf = session.csrf
	}
	return fixture.call(t, http.MethodPost, "/rpc", map[string]any{"cmd": command, "args": args}, session, csrf, headers)
}

func aiRPCErrorCode(call accountCall) string {
	if call.body["ok"] == true {
		return ""
	}
	errorBody, _ := call.body["error"].(map[string]any)
	code, _ := errorBody["code"].(string)
	return code
}

func requireAIOwnerForbidden(t *testing.T, call accountCall) {
	t.Helper()
	if code := aiRPCErrorCode(call); code != string(ipc.CodeForbidden) {
		t.Fatalf("rpc = %+v, want forbidden", call.body)
	}
}

func TestGatewayAIOwnerBoundary(t *testing.T) {
	fixture, database, _ := newAIOwnerRPCFixture(t)
	seedLegacyAIState(t, database)
	for _, test := range []struct {
		command string
		args    any
	}{
		{command: syncservice.CommandCollectAIProfiles, args: map[string]any{"args": map[string]any{"revealSecrets": true}}},
		{command: syncservice.CommandCollectAIPermission, args: map[string]any{}},
		{command: syncservice.CommandCollectAIGrants, args: map[string]any{}},
	} {
		requireAIOwnerForbidden(t, aiRPC(t, fixture, nil, test.command, test.args, true))
	}

	call := aiRPC(t, fixture, nil, syncservice.CommandCollectTombstones, map[string]any{"args": map[string]any{}}, true)
	if call.body["ok"] != true {
		t.Fatalf("non-AI tombstone collection should remain available: %+v", call.body)
	}
	data, _ := call.body["data"].(map[string]any)
	rows, _ := data["tombstones"].([]any)
	if len(rows) != 1 {
		t.Fatalf("tombstones = %+v, want only the non-AI row", rows)
	}
	row, _ := rows[0].(map[string]any)
	if row["targetKind"] != "group" {
		t.Fatalf("gateway exposed an AI tombstone: %+v", row)
	}

	groupID := ids.New()
	applyArgs := map[string]any{"args": map[string]any{"objects": []map[string]any{
		{"id": "global", "kind": "ai_permission", "payload": map[string]any{"id": "global", "mode": "read_write", "dangerRules": []string{}, "updatedAt": 200}},
		{"id": groupID, "kind": "group", "payload": map[string]any{"id": groupID, "name": "must-not-write", "sort": 0, "createdAt": 1, "updatedAt": 1}},
	}}}
	requireAIOwnerForbidden(t, aiRPC(t, fixture, nil, syncservice.CommandApplyObjects, applyArgs, true))
	if _, err := database.GroupGet(context.Background(), groupID); err == nil {
		t.Fatal("gateway apply wrote a group before the owner failure")
	}
	tombstoneArgs := map[string]any{"args": map[string]any{"objects": []map[string]any{
		{"id": "global", "kind": "tombstone", "payload": map[string]any{"targetKind": "ai_permission", "deletedAt": 999}},
	}}}
	requireAIOwnerForbidden(t, aiRPC(t, fixture, nil, syncservice.CommandApplyObjects, tombstoneArgs, true))
	for key, want := range map[string]string{
		store.AIProfilesSettingKey:   `{"version":1,"profiles":[{"id":"legacy","name":"legacy","apiKey":"legacy-secret","model":"m1"}],"activeId":"legacy"}`,
		guard.PermissionSettingKey:   `{"mode":"silent","dangerRules":[]}`,
		guard.DeviceGrantsSettingKey: `{"legacy":{"deviceId":"legacy","kinds":["terminal_write"],"updatedAt":1}}`,
	} {
		got, found, err := database.SettingGet(context.Background(), key)
		if err != nil || !found || got != want {
			t.Fatalf("legacy setting %s changed: found=%v got=%q err=%v", key, found, got, err)
		}
	}
}

func TestDesktopAndOpenAIOwnerAccess(t *testing.T) {
	_, database, syncService := newAIOwnerRPCFixture(t)
	seedLegacyAIState(t, database)
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "explicit open", ctx: owner.WithOpenAccess(context.Background())},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := syncService.CollectAIProfiles(test.ctx, syncservice.CollectAIProfilesRequest{}); err != nil {
				t.Fatal(err)
			}
			if _, err := syncService.CollectAIPermission(test.ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := syncService.CollectAIGrants(test.ctx); err != nil {
				t.Fatal(err)
			}
			result, err := syncService.CollectTombstones(test.ctx, syncservice.CollectTombstonesRequest{})
			if err != nil || len(result.Tombstones) != 2 {
				t.Fatalf("open tombstones = %+v, err=%v", result.Tombstones, err)
			}
			apply := syncservice.ApplyObjectsRequest{Objects: []syncservice.ApplyObject{{
				ID: "global", Kind: syncservice.KindAIPermission,
				Payload: json.RawMessage(`{"id":"global","mode":"read_write","dangerRules":[],"updatedAt":9000000000000}`),
			}}}
			applied, err := syncService.ApplyObjects(test.ctx, apply)
			if err != nil || applied.Applied != 1 {
				t.Fatalf("open apply = %+v, err=%v", applied, err)
			}
			remove := syncservice.ApplyObjectsRequest{Objects: []syncservice.ApplyObject{{
				ID: "global", Kind: syncservice.KindTombstone,
				Payload: json.RawMessage(`{"targetKind":"ai_permission","deletedAt":9000000000001}`),
			}}}
			removed, err := syncService.ApplyObjects(test.ctx, remove)
			if err != nil || removed.Applied != 1 {
				t.Fatalf("open tombstone apply = %+v, err=%v", removed, err)
			}
		})
	}
	desktop := syncservice.New(database, nil, syncservice.WithMetadata("desktop", true))
	if _, err := desktop.CollectAIProfiles(context.Background(), syncservice.CollectAIProfilesRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.CollectAIPermission(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := desktop.CollectAIGrants(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSyncAIGrantForeignAndMissingTarget(t *testing.T) {
	_, database, syncService := newAIOwnerRPCFixture(t)
	ctx := context.Background()
	for _, row := range []struct {
		id    string
		owner string
	}{{"asset-a", "user-a"}, {"asset-b", "user-b"}} {
		if _, err := database.DB().ExecContext(ctx, `INSERT INTO asset(id,kind,name,options_json,sort,created_at,updated_at,owner_id) VALUES(?,?,?,?,?,?,?,?)`,
			row.id, "ssh", row.id, "{}", 0, 1, 1, row.owner); err != nil {
			t.Fatal(err)
		}
	}
	foreign := syncservice.ApplyObjectsRequest{Objects: []syncservice.ApplyObject{{
		ID: "device/asset-b", Kind: syncservice.KindAIGrant,
		Payload: json.RawMessage(`{"id":"device/asset-b","deviceId":" asset-b ","kinds":["terminal_write"],"updatedAt":100}`),
	}}}
	if _, err := syncService.ApplyObjects(ipc.WithUserID(ctx, "user-a"), foreign); err == nil {
		t.Fatal("foreign grant target was applied")
	}
	if _, found, err := database.SettingGet(ctx, "ai.device_grants.user.user-a"); err != nil || found {
		t.Fatalf("foreign apply changed settings: found=%v err=%v", found, err)
	}
	missing := syncservice.ApplyObjectsRequest{Objects: []syncservice.ApplyObject{{
		ID: "device/asset-not-arrived", Kind: syncservice.KindAIGrant,
		Payload: json.RawMessage(`{"id":"device/asset-not-arrived","deviceId":"asset-not-arrived","kinds":["terminal_write"],"updatedAt":100}`),
	}}}
	result, err := syncService.ApplyObjects(ipc.WithUserID(ctx, "user-a"), missing)
	if err != nil || result.Applied != 1 {
		t.Fatalf("missing target must remain convergent: result=%+v err=%v", result, err)
	}
}

func TestSyncAIOwnerTwoAccountsOverRPC(t *testing.T) {
	fixture, database, _ := newAIOwnerRPCFixture(t)
	ctx := context.Background()
	alice, err := fixture.accounts.CreateUser(ctx, "alice", "Alice", "alice-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := fixture.accounts.CreateUser(ctx, "bob", "Bob", "bob-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		userID string
		mode   string
	}{{alice.ID, "silent"}, {bob.ID, "read_only"}} {
		if err := database.SettingSet(ctx, "ai.permission.user."+row.userID, `{"mode":"`+row.mode+`","dangerRules":[]}`); err != nil {
			t.Fatal(err)
		}
		if err := database.SettingSet(ctx, "user.ai.models."+row.userID, `{"version":1,"profiles":[{"id":"p-`+row.mode+`","name":"profile-`+row.mode+`","apiKey":"","model":"m1"}],"activeId":"p-`+row.mode+`"}`); err != nil {
			t.Fatal(err)
		}
		if err := database.SettingSet(ctx, "ai.device_grants.user."+row.userID, `{"device-`+row.mode+`":{"deviceId":"device-`+row.mode+`","kinds":["terminal_write"],"updatedAt":1}}`); err != nil {
			t.Fatal(err)
		}
	}
	aliceSession := fixture.login(t, "alice", "alice-pw-123")
	bobSession := fixture.login(t, "bob", "bob-pw-123")
	for _, test := range []struct {
		session *accountTestSession
		mode    string
	}{{aliceSession, "silent"}, {bobSession, "read_only"}} {
		call := aiRPC(t, fixture, test.session, syncservice.CommandCollectAIPermission, map[string]any{}, false)
		if call.body["ok"] != true {
			t.Fatalf("collect permission failed: %+v", call.body)
		}
		data, _ := call.body["data"].(map[string]any)
		if data["mode"] != test.mode {
			t.Fatalf("permission = %+v, want %s", data, test.mode)
		}
	}

	for _, test := range []struct {
		session *accountTestSession
		mode    string
	}{{aliceSession, "silent"}, {bobSession, "read_only"}} {
		profilesCall := aiRPC(t, fixture, test.session, syncservice.CommandCollectAIProfiles, map[string]any{"args": map[string]any{}}, false)
		if profilesCall.body["ok"] != true {
			t.Fatalf("collect profiles failed: %+v", profilesCall.body)
		}
		profileData, _ := profilesCall.body["data"].(map[string]any)
		profileRows, _ := profileData["profiles"].([]any)
		if len(profileRows) != 1 || profileRows[0].(map[string]any)["name"] != "profile-"+test.mode {
			t.Fatalf("profiles = %+v, want only profile-%s", profileRows, test.mode)
		}
		grantsCall := aiRPC(t, fixture, test.session, syncservice.CommandCollectAIGrants, map[string]any{}, false)
		if grantsCall.body["ok"] != true {
			t.Fatalf("collect grants failed: %+v", grantsCall.body)
		}
		grantData, _ := grantsCall.body["data"].(map[string]any)
		grantRows, _ := grantData["grants"].([]any)
		if len(grantRows) != 1 || grantRows[0].(map[string]any)["deviceId"] != "device-"+test.mode {
			t.Fatalf("grants = %+v, want only device-%s", grantRows, test.mode)
		}
	}

	for _, test := range []struct {
		session *accountTestSession
		userID  string
		mode    string
		at      int64
	}{{aliceSession, alice.ID, "read_write", 9000000000000}, {bobSession, bob.ID, "silent", 9000000000001}} {
		args := map[string]any{"args": map[string]any{"objects": []map[string]any{{
			"id": "global", "kind": "ai_permission",
			"payload": map[string]any{"id": "global", "mode": test.mode, "dangerRules": []string{}, "updatedAt": test.at},
		}}}}
		call := aiRPC(t, fixture, test.session, syncservice.CommandApplyObjects, args, false)
		if call.body["ok"] != true {
			t.Fatalf("apply for %s failed: %+v", test.userID, call.body)
		}
	}
	for _, row := range []struct {
		userID string
		mode   string
	}{{alice.ID, "read_write"}, {bob.ID, "silent"}} {
		value, found, err := database.SettingGet(ctx, "ai.permission.user."+row.userID)
		if err != nil || !found || !strings.Contains(value, `"mode":"`+row.mode+`"`) {
			t.Fatalf("permission for %s = %q, found=%v err=%v", row.userID, value, found, err)
		}
	}
}
