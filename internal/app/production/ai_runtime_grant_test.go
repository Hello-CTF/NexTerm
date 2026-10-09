package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
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
	services := &ProductionServices{Store: database, Profiles: profileManager, Sessions: sessions}
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
