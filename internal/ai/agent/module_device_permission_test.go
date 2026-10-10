package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

type deviceModeTerminal struct {
	assetID string
}

func (t *deviceModeTerminal) Snapshot(context.Context, string) (tools.Screen, error) {
	return tools.Screen{}, nil
}

func (t *deviceModeTerminal) Write(context.Context, string, []byte) error { return nil }

func (t *deviceModeTerminal) SessionAssetID(context.Context, string) (string, error) {
	return t.assetID, nil
}

func deviceModeConfig(t *testing.T) (Config, *guard.Manager) {
	t.Helper()
	manager, err := guard.NewManager(context.Background(), &grantSettings{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(tools.Dependencies{
		Terminal:   &deviceModeTerminal{assetID: "asset-a"},
		TabSession: func(string) string { return "sess-1" },
	})
	return Config{Permissions: manager, Tools: registry}, manager
}

func permissionOf(t *testing.T, data json.RawMessage) guard.Config {
	t.Helper()
	var config guard.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestDevicePermissionCommandsScopeToDevice(t *testing.T) {
	config, manager := deviceModeConfig(t)
	dispatcher := newGrantDispatcher(t, config)

	global := dispatchGrant(t, dispatcher, "ai_get_permission", `{}`)
	if !global.OK || permissionOf(t, global.Data).Mode != guard.ReadWrite {
		t.Fatalf("global default = %+v", global)
	}

	set := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"silent","dangerRules":[]},"scope":{"sessionId":"sess-1"}}`)
	if !set.OK {
		t.Fatalf("device set failed: %+v", set.Error)
	}
	if got := permissionOf(t, set.Data); got.Mode != guard.Silent {
		t.Fatalf("device set response = %+v", got)
	}
	if mode, ok := manager.DeviceMode("asset-a"); !ok || mode != guard.Silent {
		t.Fatalf("DeviceMode = %v %v", mode, ok)
	}
	if got := manager.Get(); got.Mode != guard.ReadWrite {
		t.Fatalf("global config must stay untouched, got %+v", got)
	}

	scoped := dispatchGrant(t, dispatcher, "ai_get_permission", `{"scope":{"tabId":"tab-9"}}`)
	if !scoped.OK || permissionOf(t, scoped.Data).Mode != guard.Silent {
		t.Fatalf("scoped get = %+v", scoped)
	}
	unscoped := dispatchGrant(t, dispatcher, "ai_get_permission", `{}`)
	if !unscoped.OK || permissionOf(t, unscoped.Data).Mode != guard.ReadWrite {
		t.Fatalf("unscoped get = %+v", unscoped)
	}

	cleared := dispatchGrant(t, dispatcher, "ai_set_permission", `{"scope":{"sessionId":"sess-1"},"followGlobal":true}`)
	if !cleared.OK {
		t.Fatalf("followGlobal failed: %+v", cleared.Error)
	}
	if _, ok := manager.DeviceMode("asset-a"); ok {
		t.Fatal("override not cleared")
	}
	if got := permissionOf(t, cleared.Data); got.Mode != guard.ReadWrite {
		t.Fatalf("cleared response = %+v", got)
	}
}

func TestDevicePermissionCommandFallbacks(t *testing.T) {
	config, manager := deviceModeConfig(t)
	dispatcher := newGrantDispatcher(t, config)

	global := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"read_only","dangerRules":["rm -rf /"]}}`)
	if !global.OK || permissionOf(t, global.Data).Mode != guard.ReadOnly {
		t.Fatalf("global set = %+v", global)
	}
	if got := manager.Get(); got.Mode != guard.ReadOnly || len(got.DangerRules) != 1 {
		t.Fatalf("global set must persist danger rules, got %+v", got)
	}

	orphan := dispatchGrant(t, dispatcher, "ai_set_permission", `{"followGlobal":true}`)
	if orphan.OK || orphan.Error == nil || !strings.Contains(orphan.Error.Message, "无法确定设备身份") {
		t.Fatalf("followGlobal without device = %+v", orphan)
	}

	invalid := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"yolo"},"scope":{"sessionId":"sess-1"}}`)
	if invalid.OK || invalid.Error == nil || !strings.Contains(invalid.Error.Message, "权限模式仅支持") {
		t.Fatalf("invalid device mode = %+v", invalid)
	}
	if _, ok := manager.DeviceMode("asset-a"); ok {
		t.Fatal("invalid mode must not persist")
	}
}

func TestDevicePermissionRejectsWriteWhenDeviceUnresolvable(t *testing.T) {
	manager, err := guard.NewManager(context.Background(), &grantSettings{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(tools.Dependencies{
		Terminal:   &deviceModeTerminal{assetID: ""},
		TabSession: func(string) string { return "" },
	})
	dispatcher := newGrantDispatcher(t, Config{Permissions: manager, Tools: registry})

	response := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"silent","dangerRules":[]},"scope":{"sessionId":"sess-gone"}}`)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "无法确定设备身份") {
		t.Fatalf("scoped set with unresolvable device = %+v", response)
	}
	if got := manager.Get(); got.Mode != guard.ReadWrite {
		t.Fatalf("global config must stay untouched, got %+v", got)
	}

	global := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"read_only","dangerRules":[]}}`)
	if !global.OK {
		t.Fatalf("unscoped global set must still work: %+v", global.Error)
	}
}

func TestDevicePermissionCommandsUnavailableWithoutManager(t *testing.T) {
	dispatcher := newGrantDispatcher(t, Config{})
	for command, args := range map[string]string{
		"ai_get_permission": `{}`,
		"ai_set_permission": `{"config":{"mode":"silent"}}`,
	} {
		response := dispatchGrant(t, dispatcher, command, args)
		if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "权限设置未配置") {
			t.Fatalf("%s without manager = %+v", command, response)
		}
	}
}
