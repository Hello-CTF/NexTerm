package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func composeGrantRuntime(t *testing.T) (*ProductionServices, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "server"})
	if err != nil {
		t.Fatal(err)
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{})
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions, desktop: true}
	if err := composeAIRuntime(ctx, services); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		services.closeAIRuntime()
		_ = sessions.Close()
		_ = database.Close()
	})
	return services, database, asset.ID
}

func composedGrantDispatch(t *testing.T, services *ProductionServices, command, args string) ipc.Response {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := agent.Module(services.Agent).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func TestProductionAgentGrantCommandsShareGrantsManager(t *testing.T) {
	services, database, assetID := composeGrantRuntime(t)

	set := composedGrantDispatch(t, services, "ai_grant_set", `{"deviceId":"`+assetID+`","kinds":["terminal_write","session_exec"]}`)
	if !set.OK {
		t.Fatalf("ai_grant_set = %+v", set.Error)
	}
	raw, found, err := database.SettingGet(context.Background(), guard.DeviceGrantsSettingKey)
	if err != nil || !found || !strings.Contains(raw, assetID) {
		t.Fatalf("grant must persist through the production grants manager: raw=%q found=%v err=%v", raw, found, err)
	}

	list := composedGrantDispatch(t, services, "ai_grant_list", `{}`)
	if !list.OK {
		t.Fatalf("ai_grant_list = %+v", list.Error)
	}
	var listed []guard.DeviceGrant
	if err := json.Unmarshal(list.Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].DeviceID != assetID || len(listed[0].Kinds) != 2 {
		t.Fatalf("listed = %+v", listed)
	}

	revoke := composedGrantDispatch(t, services, "ai_grant_revoke", `{"deviceId":"`+assetID+`"}`)
	if !revoke.OK {
		t.Fatalf("ai_grant_revoke = %+v", revoke.Error)
	}

	kind := "ai_grant"
	rows, err := database.AuditQuery(context.Background(), store.AuditQuery{Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("grant/revoke must write ai_grant audit rows, got %d", len(rows))
	}
	payloads := rows[0].PayloadJSON + rows[1].PayloadJSON
	if !strings.Contains(payloads, `"action":"grant"`) || !strings.Contains(payloads, `"action":"revoke"`) {
		t.Fatalf("audit payloads = %q", payloads)
	}
}

func TestProductionPermanentGrantAndTargetValidation(t *testing.T) {
	adminA := ipc.WithRole(ipc.WithUserID(context.Background(), "owner-a"), "superadmin")
	adminB := ipc.WithRole(ipc.WithUserID(context.Background(), "owner-b"), "superadmin")
	regularA := ipc.WithRole(ipc.WithUserID(context.Background(), "owner-a"), "user")
	services, database, _, sessionID, _ := composeOutcomeRuntimeContext(t, "systemctl restart nginx", adminA, false)
	connected, err := services.Sessions.SessionFor(adminA, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	assetA := connected.Asset().ID
	assetB, err := database.AssetCreate(adminB, store.AssetInput{Kind: "ssh", Name: "owner-b-server"})
	if err != nil {
		t.Fatal(err)
	}

	confirmPersistent := func(ctx context.Context) []agent.Event {
		t.Helper()
		stream := &agent.SliceStream{}
		response, err := services.Agent.Start(ctx, agent.ChatArgs{Message: "run it", Scope: tools.Scope{SessionID: sessionID}}, agent.StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		confirmation := waitOutcomeEvent(t, stream, "confirmRequired")
		if err := services.Agent.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow_persistent"}); err != nil {
			t.Fatal(err)
		}
		return waitOutcomeClosed(t, stream)
	}

	permission, err := services.Guard.Snapshot(regularA)
	if err != nil || permission.Mode != guard.ReadWrite {
		t.Fatalf("initial permission = %+v, %v", permission, err)
	}
	if _, found, err := database.SettingGet(regularA, guard.DeviceGrantsSettingKeyForUser(regularA)); err != nil || found {
		t.Fatalf("unexpected initial device grants: found=%v err=%v", found, err)
	}
	if _, found, err := database.SettingGet(regularA, guard.GrantRulesSettingKeyForUser(regularA)); err != nil || found {
		t.Fatalf("unexpected initial grant rules: found=%v err=%v", found, err)
	}
	regularEvents := confirmPersistent(regularA)
	regularAllowed := false
	for _, event := range regularEvents {
		if event.Type == "toolResult" && event.OK {
			regularAllowed = true
		}
		if event.Type == "error" {
			t.Fatalf("regular user permanent grant failed: %s", event.Message)
		}
	}
	if !regularAllowed {
		t.Fatalf("regular user permanent grant did not complete: %+v", regularEvents)
	}
	dispatcher := ipc.NewDispatcher()
	if err := agent.Module(services.Agent).RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	if response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_grant_rule_list", Args: json.RawMessage(`{}`)}, ipc.Environment{}); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("ownerless server rule list = %+v, want forbidden", response)
	}
	if _, err := services.Profiles.Overview(context.Background()); err == nil {
		t.Fatal("ownerless server profile request used shared state")
	}
	listRules := func(ctx context.Context) []guard.GrantRule {
		t.Helper()
		response := dispatcher.Dispatch(ctx, ipc.Request{Command: "ai_grant_rule_list", Args: json.RawMessage(`{}`)}, ipc.Environment{})
		if !response.OK {
			t.Fatalf("rule list = %+v", response.Error)
		}
		var rules []guard.GrantRule
		if err := json.Unmarshal(response.Data, &rules); err != nil {
			t.Fatal(err)
		}
		return rules
	}
	rulesA := listRules(adminA)
	if len(rulesA) != 1 || rulesA[0].DeviceID != assetA || rulesA[0].Action != "exec_commands" || rulesA[0].ExpiresAt != 0 {
		t.Fatalf("owner A rules = %+v", rulesA)
	}
	if rulesB := listRules(adminB); len(rulesB) != 0 {
		t.Fatalf("owner B sees foreign rules: %+v", rulesB)
	}

	dispatch := func(ctx context.Context, command, args string) ipc.Response {
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	if response := dispatch(regularA, "ai_grant_set", `{"deviceId":"`+assetA+`","kinds":["terminal_write"]}`); !response.OK {
		t.Fatalf("regular user grant on own asset = %+v", response.Error)
	}
	if response := dispatch(regularA, "ai_grant_set", `{"deviceId":"`+assetB.ID+`","kinds":["terminal_write"]}`); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("regular user grant on foreign asset = %+v, want forbidden", response)
	}
	if response := dispatch(regularA, "ai_grant_revoke", `{"deviceId":"`+assetB.ID+`"}`); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("regular user revoke on foreign asset = %+v, want forbidden", response)
	}
	listed := dispatch(regularA, "ai_grant_list", `{}`)
	if !listed.OK {
		t.Fatalf("regular user grant list = %+v", listed.Error)
	}
	var ownGrants []guard.DeviceGrant
	if err := json.Unmarshal(listed.Data, &ownGrants); err != nil {
		t.Fatal(err)
	}
	if len(ownGrants) != 1 || ownGrants[0].DeviceID != assetA {
		t.Fatalf("regular user grants = %+v", ownGrants)
	}
	if response := dispatch(regularA, "ai_grant_revoke", `{"deviceId":"`+assetA+`"}`); !response.OK {
		t.Fatalf("regular user revoke on own asset = %+v", response.Error)
	}

	if err := services.Guard.SetDeviceMode(regularA, assetB.ID, guard.ReadOnly); err == nil {
		t.Fatal("regular user set a mode on another owner's asset")
	}
	if err := services.Guard.SetDeviceMode(regularA, assetA, guard.Silent); err != nil {
		t.Fatalf("regular user cannot set a mode on own asset: %v", err)
	}
	if err := services.Guard.SetDeviceMode(regularA, assetA, ""); err != nil {
		t.Fatal(err)
	}
	for _, user := range []store.AppUser{
		{ID: "owner-a", Username: "owner-a", Role: "superadmin", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1},
		{ID: "owner-b", Username: "owner-b", Role: "superadmin", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := database.GORM().Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, device := range []store.UserDevice{
		{ID: "device-a", UserID: "owner-a", Name: "A device", Kind: "agent", CreatedAt: 1},
		{ID: "device-b", UserID: "owner-b", Name: "B device", Kind: "agent", CreatedAt: 1},
	} {
		if err := database.GORM().Create(&device).Error; err != nil {
			t.Fatal(err)
		}
	}
	if response := dispatch(regularA, "ai_grant_set", `{"deviceId":"device/device-a","kinds":["terminal_write"]}`); !response.OK {
		t.Fatalf("regular user grant on own device = %+v", response.Error)
	}
	if response := dispatch(regularA, "ai_grant_set", `{"deviceId":"device/device-b","kinds":["terminal_write"]}`); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("regular user grant on foreign device = %+v, want forbidden", response)
	}
	if response := dispatch(regularA, "ai_grant_revoke", `{"deviceId":"device/device-b"}`); response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("regular user revoke on foreign device = %+v, want forbidden", response)
	}
	if response := dispatch(regularA, "ai_grant_revoke", `{"deviceId":"device/device-a"}`); !response.OK {
		t.Fatalf("regular user revoke on own device = %+v", response.Error)
	}
	if err := services.Guard.SetDeviceMode(regularA, "device/device-b", guard.ReadOnly); err == nil {
		t.Fatal("regular user set a mode on another owner's device")
	}
	if err := services.Guard.SetDeviceMode(regularA, "device/device-a", guard.Silent); err != nil {
		t.Fatalf("regular user cannot set a mode on own device: %v", err)
	}
	if err := services.Guard.SetDeviceMode(regularA, "device/device-a", ""); err != nil {
		t.Fatal(err)
	}
}
