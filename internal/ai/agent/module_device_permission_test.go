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

func stateOf(t *testing.T, data json.RawMessage) PermissionState {
	t.Helper()
	var state PermissionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// A mode chosen in a terminal's panel is the global mode: it never creates a
// host override, so every other host (and the global default) follows it.
func TestSetPermissionWithScopeChangesGlobalMode(t *testing.T) {
	config, manager := deviceModeConfig(t)
	dispatcher := newGrantDispatcher(t, config)

	set := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"full_access","dangerRules":[]},"scope":{"sessionId":"sess-1"}}`)
	if !set.OK {
		t.Fatalf("set failed: %+v", set.Error)
	}
	state := stateOf(t, set.Data)
	if state.Config.Mode != guard.FullAccess || state.GlobalMode != guard.FullAccess || state.Source != PermissionSourceGlobal || !state.HostResolved || state.HostMode != "" {
		t.Fatalf("set response = %+v", state)
	}
	if _, ok := mustAgentDeviceMode(t, manager, "asset-a"); ok {
		t.Fatal("a plain mode switch created a host override")
	}
	if got := mustAgentPermission(t, manager); got.Mode != guard.FullAccess {
		t.Fatalf("global mode = %+v", got)
	}
	// Another terminal and the unscoped panel see the same mode.
	for _, args := range []string{`{"scope":{"tabId":"tab-9"}}`, `{}`} {
		response := dispatchGrant(t, dispatcher, "ai_permission_state", args)
		if !response.OK || stateOf(t, response.Data).Config.Mode != guard.FullAccess {
			t.Fatalf("state %s = %+v", args, response)
		}
	}
}

func TestHostOverrideIsExplicitAndVisible(t *testing.T) {
	config, manager := deviceModeConfig(t)
	dispatcher := newGrantDispatcher(t, config)

	set := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"read_only","dangerRules":[]},"scope":{"sessionId":"sess-1"},"hostOnly":true}`)
	if !set.OK {
		t.Fatalf("host set failed: %+v", set.Error)
	}
	state := stateOf(t, set.Data)
	if state.Config.Mode != guard.ReadOnly || state.HostMode != guard.ReadOnly || state.GlobalMode != guard.ReadWrite || state.Source != PermissionSourceHost {
		t.Fatalf("host set response = %+v", state)
	}
	if mode, ok := mustAgentDeviceMode(t, manager, "asset-a"); !ok || mode != guard.ReadOnly {
		t.Fatalf("DeviceMode = %v %v", mode, ok)
	}
	if got := mustAgentPermission(t, manager); got.Mode != guard.ReadWrite {
		t.Fatalf("global config must stay untouched, got %+v", got)
	}
	// The older command still reports the effective mode of a scope.
	scoped := dispatchGrant(t, dispatcher, "ai_get_permission", `{"scope":{"tabId":"tab-9"}}`)
	if !scoped.OK || permissionOf(t, scoped.Data).Mode != guard.ReadOnly {
		t.Fatalf("scoped get = %+v", scoped)
	}

	// Changing the global mode leaves the explicit override in force and reports it.
	global := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"silent","dangerRules":[]},"scope":{"sessionId":"sess-1"}}`)
	if state := stateOf(t, global.Data); !global.OK || state.Config.Mode != guard.ReadOnly || state.GlobalMode != guard.Silent || state.Source != PermissionSourceHost {
		t.Fatalf("global set with override = %+v", global)
	}

	cleared := dispatchGrant(t, dispatcher, "ai_set_permission", `{"scope":{"sessionId":"sess-1"},"followGlobal":true}`)
	if !cleared.OK {
		t.Fatalf("followGlobal failed: %+v", cleared.Error)
	}
	if _, ok := mustAgentDeviceMode(t, manager, "asset-a"); ok {
		t.Fatal("override not cleared")
	}
	if state := stateOf(t, cleared.Data); state.Config.Mode != guard.Silent || state.Source != PermissionSourceGlobal {
		t.Fatalf("cleared response = %+v", state)
	}
}

func TestDevicePermissionCommandFallbacks(t *testing.T) {
	config, manager := deviceModeConfig(t)
	dispatcher := newGrantDispatcher(t, config)

	global := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"read_only","dangerRules":["rm -rf /"]}}`)
	if !global.OK || stateOf(t, global.Data).Config.Mode != guard.ReadOnly {
		t.Fatalf("global set = %+v", global)
	}
	if got := mustAgentPermission(t, manager); got.Mode != guard.ReadOnly || len(got.DangerRules) != 1 {
		t.Fatalf("global set must persist danger rules, got %+v", got)
	}

	for _, args := range []string{`{"followGlobal":true}`, `{"config":{"mode":"silent"},"hostOnly":true}`} {
		orphan := dispatchGrant(t, dispatcher, "ai_set_permission", args)
		if orphan.OK || orphan.Error == nil || !strings.Contains(orphan.Error.Message, "无法确定当前主机") {
			t.Fatalf("%s without host = %+v", args, orphan)
		}
	}

	invalid := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"yolo"},"scope":{"sessionId":"sess-1"},"hostOnly":true}`)
	if invalid.OK || invalid.Error == nil || !strings.Contains(invalid.Error.Message, "权限模式仅支持") {
		t.Fatalf("invalid device mode = %+v", invalid)
	}
	if _, ok := mustAgentDeviceMode(t, manager, "asset-a"); ok {
		t.Fatal("invalid mode must not persist")
	}
}

// A terminal whose host cannot be resolved still edits the global mode; only a
// host override needs the host.
func TestPermissionStateWithUnresolvableHost(t *testing.T) {
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
	if state := stateOf(t, response.Data); !response.OK || state.Config.Mode != guard.Silent || state.HostResolved {
		t.Fatalf("scoped set with unresolvable host = %+v", response)
	}
	if got := mustAgentPermission(t, manager); got.Mode != guard.Silent {
		t.Fatalf("global mode = %+v", got)
	}
	hostOnly := dispatchGrant(t, dispatcher, "ai_set_permission", `{"config":{"mode":"read_only","dangerRules":[]},"scope":{"sessionId":"sess-gone"},"hostOnly":true}`)
	if hostOnly.OK || hostOnly.Error == nil || !strings.Contains(hostOnly.Error.Message, "无法确定当前主机") {
		t.Fatalf("host override with unresolvable host = %+v", hostOnly)
	}
}

func TestDevicePermissionCommandsUnavailableWithoutManager(t *testing.T) {
	dispatcher := newGrantDispatcher(t, Config{})
	for command, args := range map[string]string{
		"ai_get_permission":   `{}`,
		"ai_set_permission":   `{"config":{"mode":"silent"}}`,
		"ai_permission_state": `{}`,
	} {
		response := dispatchGrant(t, dispatcher, command, args)
		if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "权限设置未配置") {
			t.Fatalf("%s without manager = %+v", command, response)
		}
	}
}
