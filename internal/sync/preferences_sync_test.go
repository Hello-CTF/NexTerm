package sync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func putPreferenceSetting(t *testing.T, device *testDevice, key, value string, updatedAt int64) {
	t.Helper()
	if _, err := device.db.DB().ExecContext(context.Background(), `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, key, value, updatedAt); err != nil {
		t.Fatal(err)
	}
}

func readPreferenceSetting(t *testing.T, device *testDevice, key string) (string, int64, bool) {
	t.Helper()
	var value string
	var updatedAt int64
	err := device.db.DB().QueryRowContext(context.Background(), "SELECT value, updated_at FROM setting WHERE key = ?", key).Scan(&value, &updatedAt)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return "", 0, false
		}
		t.Fatal(err)
	}
	return value, updatedAt, true
}

func TestEngineAIPermissionAndPreferencesConverge(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB} {
		enableDeviceKindOptIn(t, device, userID)
	}

	putPreferenceSetting(t, deviceA, guard.PermissionSettingKey, `{"mode":"silent","dangerRules":["rm -rf /"]}`, 100)
	putPreferenceSetting(t, deviceA, "prefs.user."+userID+".keybinding.newTerminal", `"Mod+Shift+t"`, 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	if value, updatedAt, found := readPreferenceSetting(t, deviceB, guard.PermissionSettingKey); !found || value != `{"mode":"silent","dangerRules":["rm -rf /"]}` || updatedAt != 100 {
		t.Fatalf("permission after first sync = %q %d %v", value, updatedAt, found)
	}
	if value, updatedAt, found := readPreferenceSetting(t, deviceB, "prefs.user."+userID+".keybinding.newTerminal"); !found || value != `"Mod+Shift+t"` || updatedAt != 100 {
		t.Fatalf("preference after first sync = %q %d %v", value, updatedAt, found)
	}

	putPreferenceSetting(t, deviceA, guard.PermissionSettingKey, `{"mode":"read_write","dangerRules":["format"]}`, 200)
	putPreferenceSetting(t, deviceA, "prefs.user."+userID+".keybinding.newTerminal", `"Mod+Shift+n"`, 200)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if value, updatedAt, found := readPreferenceSetting(t, deviceB, guard.PermissionSettingKey); !found || value != `{"mode":"read_write","dangerRules":["format"]}` || updatedAt != 200 {
		t.Fatalf("permission after update = %q %d %v", value, updatedAt, found)
	}
	if value, updatedAt, found := readPreferenceSetting(t, deviceB, "prefs.user."+userID+".keybinding.newTerminal"); !found || value != `"Mod+Shift+n"` || updatedAt != 200 {
		t.Fatalf("preference after update = %q %d %v", value, updatedAt, found)
	}

	if err := deviceA.engine.syncTombstonePut(context.Background(), "global", KindAIPermission, 300); err != nil {
		t.Fatal(err)
	}
	if _, err := account.NewPreferences(deviceA.db).UpdateUserOverrides(context.Background(), userID, nil, []string{"keybinding.newTerminal"}); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "global", KindTombstone)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	value, updatedAt, found := readPreferenceSetting(t, deviceB, guard.PermissionSettingKey)
	if found {
		t.Fatalf("permission was not deleted by tombstone: %q %d", value, updatedAt)
	}
	if _, _, found := readPreferenceSetting(t, deviceB, "prefs.user."+userID+".keybinding.newTerminal"); found {
		t.Fatal("preference was not deleted by tombstone")
	}

	restoredAt := time.Now().UnixMilli()
	putPreferenceSetting(t, deviceA, guard.PermissionSettingKey, `{"mode":"silent","dangerRules":["rm -rf /"]}`, restoredAt)
	putPreferenceSetting(t, deviceA, "prefs.user."+userID+".keybinding.newTerminal", `"Mod+Shift+t"`, restoredAt)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "global", KindAIPermission)
	if value, _, found := readPreferenceSetting(t, deviceB, guard.PermissionSettingKey); !found || value != `{"mode":"silent","dangerRules":["rm -rf /"]}` {
		t.Fatalf("permission was not restored: %q %v", value, found)
	}
	if value, _, found := readPreferenceSetting(t, deviceB, "prefs.user."+userID+".keybinding.newTerminal"); !found || value != `"Mod+Shift+t"` {
		t.Fatalf("preference was not restored: %q %v", value, found)
	}
	var tombstones int
	if err := deviceB.db.DB().QueryRowContext(context.Background(), "SELECT count(*) FROM sync_tombstone WHERE id IN (?, ?)", "global", "preference:keybinding.newTerminal").Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("tombstones were not cleared after restoring: %d", tombstones)
	}
}

func TestPreferenceViewAndUpdate(t *testing.T) {
	device := newTestDevice(t)
	ctx := WithUserID(context.Background(), ids.New())
	key := "keybinding.newTerminal"
	service := New(device.db, device.vault)
	view, err := service.PreferenceUpdate(ctx, PreferenceUpdate{Set: map[string]json.RawMessage{key: json.RawMessage(`"Mod+Shift+n"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(view.Overrides[key]); got != `"Mod+Shift+n"` {
		t.Fatalf("override = %q, want %q", got, `"Mod+Shift+n"`)
	}
	if got := string(view.Effective[key]); got != `"Mod+Shift+n"` {
		t.Fatalf("effective = %q, want %q", got, `"Mod+Shift+n"`)
	}
	view, err = service.PreferenceView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(view.Overrides[key]); got != `"Mod+Shift+n"` {
		t.Fatalf("override after reload = %q, want %q", got, `"Mod+Shift+n"`)
	}
}

func TestPreferenceUpdateWritesTombstone(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	userID := ids.New()
	if err := device.db.SetPreferencesSyncOptIn(ctx, userID, true); err != nil {
		t.Fatal(err)
	}
	key := "keybinding.newTerminal"
	putPreferenceSetting(t, device, "prefs.user."+userID+"."+key, `"Mod+Shift+t"`, 100)
	if _, err := account.NewPreferences(device.db).UpdateUserOverrides(ctx, userID, nil, []string{key}); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := device.db.DB().QueryRowContext(ctx, "SELECT kind FROM sync_tombstone WHERE id = ?", "preference:"+key).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != KindPreference {
		t.Fatalf("tombstone kind = %q, want %q", kind, KindPreference)
	}
}

func TestPreferenceCommandsNestedContract(t *testing.T) {
	instance := newTestInstance(t, true)
	dispatcher := ipc.NewDispatcher()
	if err := instance.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	ctx := WithUserID(context.Background(), ids.New())
	response := dispatcher.Dispatch(ctx, ipc.Request{
		Command: CommandPreferencesSet,
		Args:    json.RawMessage(`{"args":{"set":{"appearance.themeMode":"light"}}}`),
	}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("nested preference update failed: %+v", response.Error)
	}
	var view PreferenceScopeView
	if err := json.Unmarshal(response.Data, &view); err != nil {
		t.Fatal(err)
	}
	if got := string(view.Overrides["appearance.themeMode"]); got != `"light"` {
		t.Fatalf("theme override = %q, want light", got)
	}
}

func TestSortObjectsForPushPreferenceKinds(t *testing.T) {
	objects := map[string]localObject{
		"transcript":    {kind: KindTranscript},
		"preference":    {kind: KindPreference},
		"tombstone":     {kind: KindTombstone},
		"ai_permission": {kind: KindAIPermission},
	}
	got := sortObjectsForPush(objects)
	want := []string{"ai_permission", "preference", "tombstone", "transcript"}
	if len(got) != len(want) {
		t.Fatalf("push order = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("push order = %v, want %v", got, want)
		}
	}
}

func TestCollectLocalObjectsHonorsPreferenceOptOut(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	userID := ids.New()
	if err := device.engine.syncTombstonePut(ctx, "global", KindAIPermission, 100); err != nil {
		t.Fatal(err)
	}
	if err := device.engine.syncTombstonePut(ctx, "preference:keybinding.newTerminal", KindPreference, 100); err != nil {
		t.Fatal(err)
	}
	objects, err := device.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{}, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("disabled preference kinds produced %d objects", len(objects))
	}
	objects, err = device.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{aiPermission: true, preferences: true}, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("enabled preference kinds produced %d objects, want 2", len(objects))
	}
}

func TestApplyPreferenceEqualRevisionUsesLocalValue(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	userID := ids.New()
	key := "keybinding.newTerminal"
	settingKey := "prefs.user." + userID + "." + key
	putPreferenceSetting(t, device, settingKey, `"Mod+Shift+t"`, 100)
	localPayload, err := marshalObject(preferenceObject{ID: "preference:" + key, Key: key, Value: json.RawMessage(`"Mod+Shift+t"`), UpdatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	var remotePayload []byte
	want := ""
	for _, candidate := range []string{`"Mod+Shift+n"`, `"Mod+Shift+p"`, `"Ctrl+Shift+x"`} {
		normalized, err := account.NormalizePreferenceValue(key, json.RawMessage(candidate))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := marshalObject(preferenceObject{ID: "preference:" + key, Key: key, Value: json.RawMessage(normalized), UpdatedAt: 100})
		if err != nil {
			t.Fatal(err)
		}
		if remoteWins(100, 100, payload, localPayload) {
			remotePayload = payload
			want = normalized
			break
		}
	}
	if remotePayload == nil {
		t.Fatal("no deterministic remote winner found")
	}
	applied, identical := device.engine.applyPreferenceObject(ctx, remotePayload, &SyncReport{}, userID)
	if !applied || identical {
		t.Fatalf("equal-revision conflict applied=%v identical=%v", applied, identical)
	}
	if value, updatedAt, found := readPreferenceSetting(t, device, settingKey); !found || value != want || updatedAt != 100 {
		t.Fatalf("preference after conflict = %q %d %v, want %q 100 true", value, updatedAt, found, want)
	}
}

func TestPreferenceTombstonesRejectInvalidIDs(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	userID := ids.New()
	if applied, _ := device.engine.applyAIPermissionTombstone(ctx, "not-global", 100, &SyncReport{}); applied {
		t.Fatal("AI permission tombstone with a non-global ID was applied")
	}
	if applied, _ := device.engine.applyPreferenceTombstone(ctx, "preference:keybinding.unknownAction", 100, &SyncReport{}, userID); applied {
		t.Fatal("unknown preference tombstone was applied")
	}
	var count int
	if err := device.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM sync_tombstone WHERE id IN (?, ?)", "not-global", "preference:keybinding.unknownAction").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid tombstones persisted: %d", count)
	}
}

func enableDeviceAIGrantOptIn(t *testing.T, device *testDevice, userID string) {
	t.Helper()
	if err := device.db.SetAIGrantSyncOptIn(context.Background(), userID, true); err != nil {
		t.Fatal(err)
	}
}

func deviceGrantsEvaluate(t *testing.T, device *testDevice, ctx context.Context, deviceID, operation string) guard.Decision {
	t.Helper()
	grants, err := guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return grants.Evaluate(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "确认"), nil, deviceID, operation, "run-1")
}

func deviceRuleEvaluate(t *testing.T, device *testDevice, ctx context.Context, deviceID, action, path string) guard.Decision {
	t.Helper()
	grants, err := guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return grants.EvaluateResource(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindWriteFS, "确认"), nil, deviceID, action, "run-1", guard.Resource{Action: action, Path: path})
}

func deviceManager(t *testing.T, device *testDevice, ctx context.Context) *guard.Manager {
	t.Helper()
	manager, err := guard.NewManager(ctx, device.db)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// AI grant sync: grants/rules/modes converge across devices with per-entity LWW, revocations propagate via tombstones,
// re-grants resurrect, and the receiving device's guard evaluation (and its Reload hook contract) picks up applied state.
func TestEngineAIGrantConverge(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB} {
		enableDeviceAIGrantOptIn(t, device, userID)
	}
	ctx := context.Background()
	userCtx := WithUserID(ctx, userID)

	// A configures a device grant, a path rule, and a per-device permission mode.
	grantsA, err := guard.NewGrants(userCtx, deviceA.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grantsA.Grant(userCtx, "dev-1", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	rule, err := grantsA.AddRule(userCtx, "dev-1", "write_file", "/var/log/**", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := deviceManager(t, deviceA, ctx).SetDeviceMode(ctx, "dev-1", guard.ReadOnly); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// B's guard evaluation allows via the synced grant and rule; the synced mode is visible.
	if decision := deviceGrantsEvaluate(t, deviceB, ctx, "dev-1", "send_keys"); decision.Action != guard.ActionAllow {
		t.Fatalf("grant must apply on B: %+v", decision)
	}
	if decision := deviceGrantsEvaluate(t, deviceB, ctx, "dev-1", "exec_commands"); decision.Action == guard.ActionAllow {
		t.Fatal("session_exec was not granted and must not be allowed")
	}
	if decision := deviceRuleEvaluate(t, deviceB, ctx, "dev-1", "write_file", "/var/log/app.log"); decision.Action != guard.ActionAllow {
		t.Fatalf("rule must apply on B: %+v", decision)
	}
	if mode, ok := deviceManager(t, deviceB, ctx).DeviceMode("dev-1"); !ok || mode != guard.ReadOnly {
		t.Fatalf("mode must apply on B: %v %v", mode, ok)
	}
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "device/dev-1", KindAIGrant)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "rule/"+rule.ID, KindAIGrant)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "mode/dev-1", KindAIGrant)

	// LWW: A widens the grant; B converges to the newer revision.
	time.Sleep(2 * time.Millisecond)
	if _, err := grantsA.Grant(userCtx, "dev-1", []guard.GrantKind{guard.GrantKindTerminalWrite, guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if decision := deviceGrantsEvaluate(t, deviceB, ctx, "dev-1", "exec_commands"); decision.Action != guard.ActionAllow {
		t.Fatalf("newer grant must win on B: %+v", decision)
	}

	// Revocation on A records a tombstone through the guard (opt-in on) and propagates to B.
	if err := grantsA.Revoke(userCtx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "device/dev-1", KindTombstone)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if decision := deviceGrantsEvaluate(t, deviceB, ctx, "dev-1", "send_keys"); decision.Action == guard.ActionAllow {
		t.Fatal("revoked grant must not allow on B")
	}

	// Re-grant resurrects the device grant (newer revision beats the tombstone); B's stale in-memory guard
	// still denies until Reload - the hook wired via SetGrantsReload.
	grantsBLive, err := guard.NewGrants(ctx, deviceB.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := grantsA.Grant(userCtx, "dev-1", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "device/dev-1", KindAIGrant)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if decision := grantsBLive.Evaluate(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "确认"), nil, "dev-1", "send_keys", "run-1"); decision.Action == guard.ActionAllow {
		t.Fatal("stale guard must deny until Reload")
	}
	if err := grantsBLive.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if decision := grantsBLive.Evaluate(ctx, guard.Config{Mode: guard.ReadWrite}, guard.Confirm(guard.KindProcess, "确认"), nil, "dev-1", "send_keys", "run-1"); decision.Action != guard.ActionAllow {
		t.Fatalf("reloaded guard must allow the resurrected grant: %+v", decision)
	}

	// Rule revocation propagates via tombstone; the mode clear propagates the same way.
	if err := grantsA.RevokeRule(userCtx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.engine.syncTombstonePut(ctx, "mode/dev-1", KindAIGrant, ids.NowMS()); err != nil {
		t.Fatal(err)
	}
	if err := deviceManager(t, deviceA, ctx).SetDeviceMode(ctx, "dev-1", ""); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "rule/"+rule.ID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "mode/dev-1", KindTombstone)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if decision := deviceRuleEvaluate(t, deviceB, ctx, "dev-1", "write_file", "/var/log/app.log"); decision.Action == guard.ActionAllow {
		t.Fatal("revoked rule must not allow on B")
	}
	if _, ok := deviceManager(t, deviceB, ctx).DeviceMode("dev-1"); ok {
		t.Fatal("cleared mode must be gone on B")
	}

	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// aiGrant opt-in gating: with the kind off, no objects or tombstones are collected; with it on, one object per entity plus tombstones.
// (The store opt-in defaults on; the off case here is the explicit opt-out.)
func TestCollectLocalObjectsHonorsAIGrantOptOut(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	userID := ids.New()
	putPreferenceSetting(t, device, guard.DeviceGrantsSettingKey, `{"dev-1":{"deviceId":"dev-1","kinds":["terminal_write"],"updatedAt":100}}`, 100)
	putPreferenceSetting(t, device, guard.GrantRulesSettingKey, `[{"id":"r-1","deviceId":"dev-1","action":"write_file","path":"/var/log/**","createdAt":100,"updatedAt":100}]`, 100)
	putPreferenceSetting(t, device, guard.DevicePermissionSettingKey, `{"dev-1":"read_only"}`, 100)
	if err := device.engine.syncTombstonePut(ctx, "device/dev-x", KindAIGrant, 100); err != nil {
		t.Fatal(err)
	}
	objects, err := device.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{}, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("disabled aiGrant produced %d objects", len(objects))
	}
	objects, err = device.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{aiGrant: true}, userID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"device/dev-1": KindAIGrant,
		"rule/r-1":     KindAIGrant,
		"mode/dev-1":   KindAIGrant,
		"device/dev-x": KindTombstone,
	}
	if len(objects) != len(want) {
		t.Fatalf("enabled aiGrant produced %d objects, want %d", len(objects), len(want))
	}
	for id, kind := range want {
		object, found := objects[id]
		if !found || object.kind != kind {
			t.Fatalf("object %s = %+v, want kind %s", id, object, kind)
		}
	}
}

// Invalid ai_grant objects and tombstones are skipped with warnings and never touch the guard settings rows.
func TestAIGrantObjectsRejectInvalidPayloads(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	engine := instance.service.engine

	if applied, _ := engine.applyAIGrantTombstone(ctx, "global", 100, &SyncReport{}); applied {
		t.Fatal("unsupported tombstone ID applied")
	}
	if applied, _ := engine.applyAIGrantTombstone(ctx, "unknown/dev-1", 100, &SyncReport{}); applied {
		t.Fatal("unknown-prefix tombstone applied")
	}
	invalid := []ApplyObject{
		{ID: "device/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantDeviceObject{ID: "device/dev-2", DeviceID: "dev-2", Kinds: []string{"terminal_write"}, UpdatedAt: 100})},
		{ID: "device/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantDeviceObject{ID: "device/dev-1", DeviceID: "dev-1", Kinds: []string{"daemon"}, UpdatedAt: 100})},
		{ID: "device/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantDeviceObject{ID: "device/dev-1", DeviceID: "dev-1", Kinds: []string{}, UpdatedAt: 100})},
		{ID: "rule/r-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantRuleObject{ID: "rule/r-1", DeviceID: "dev-1", Action: "write_file", CreatedAt: 100, UpdatedAt: 100})},
		{ID: "rule/r-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantRuleObject{ID: "rule/r-1", DeviceID: "dev-1", Action: "send_keys", Path: "/x", CreatedAt: 100, UpdatedAt: 100})},
		{ID: "rule/r-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantRuleObject{ID: "rule/r-1", DeviceID: "dev-1", Action: "docker_control", CreatedAt: 100, UpdatedAt: 100})},
		{ID: "mode/dev-1", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantModeObject{ID: "mode/dev-1", DeviceID: "dev-1", Mode: "yolo", UpdatedAt: 100})},
		{ID: "what/dev-1", Kind: KindAIGrant, Payload: json.RawMessage(`{"id":"what/dev-1","deviceId":"dev-1","updatedAt":100}`)},
	}
	for _, object := range invalid {
		result := mustApplyObjects(t, instance, object)
		entry := result.Objects[0]
		if entry.Result != ApplyResultSkipped || entry.Warning == "" {
			t.Fatalf("object %s = %+v, want skipped with warning", object.ID, entry)
		}
	}
	for _, key := range []string{guard.DeviceGrantsSettingKey, guard.GrantRulesSettingKey, guard.DevicePermissionSettingKey} {
		if _, _, found := readPreferenceSetting(t, &testDevice{db: instance.db}, key); found {
			t.Fatalf("invalid payloads wrote %s", key)
		}
	}
	var count int
	if err := instance.db.DB().QueryRowContext(ctx, "SELECT count(*) FROM sync_tombstone").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid tombstones persisted: %d", count)
	}
}

// Equal-revision ai_grant conflicts are decided by the payload hash tiebreak, deterministically on both sides.
func TestApplyAIGrantEqualRevisionUsesHashTiebreak(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	putPreferenceSetting(t, device, guard.GrantRulesSettingKey, `[{"id":"r-1","deviceId":"dev-1","action":"write_file","path":"/var/log/**","createdAt":100,"updatedAt":100}]`, 100)
	localPayload, err := marshalObject(aiGrantRuleObject{ID: "rule/r-1", DeviceID: "dev-1", Action: "write_file", Path: "/var/log/**", CreatedAt: 100, UpdatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	var remotePayload []byte
	var wantCreatedAt int64
	for candidate := int64(99); candidate >= 0; candidate-- {
		payload, err := marshalObject(aiGrantRuleObject{ID: "rule/r-1", DeviceID: "dev-1", Action: "write_file", Path: "/var/log/**", CreatedAt: candidate, UpdatedAt: 100})
		if err != nil {
			t.Fatal(err)
		}
		if remoteWins(100, 100, payload, localPayload) {
			remotePayload = payload
			wantCreatedAt = candidate
			break
		}
	}
	if remotePayload == nil {
		t.Fatal("no deterministic remote winner found")
	}
	applied, identical := device.engine.applyAIGrantRule(ctx, remotePayload, &SyncReport{})
	if !applied || identical {
		t.Fatalf("equal-revision conflict applied=%v identical=%v", applied, identical)
	}
	grants, err := guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range grants.Rules() {
		if rule.ID == "r-1" {
			if rule.CreatedAt != wantCreatedAt {
				t.Fatalf("rule after conflict createdAt = %d, want %d", rule.CreatedAt, wantCreatedAt)
			}
			return
		}
	}
	t.Fatal("rule lost after conflict")
}

// Payloads carrying edge-space DeviceID/Path are normalized exactly like the guard trims local writes; the object ID must
// match the normalized form (edge-space IDs are rejected), so LWW and tombstone identity consistently use the trimmed form.
func TestApplyAIGrantNormalizesEdgeSpaces(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	engine := device.engine

	grantPayload, err := marshalObject(aiGrantDeviceObject{ID: "device/dev-1", DeviceID: " dev-1 ", Kinds: []string{"terminal_write"}, UpdatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	if applied, _ := engine.applyAIGrantDevice(ctx, grantPayload, &SyncReport{}); !applied {
		t.Fatal("edge-space grant payload was not applied")
	}
	rulePayload, err := marshalObject(aiGrantRuleObject{ID: "rule/r-1", DeviceID: " dev-1 ", Action: "write_file", Path: " /var/log/** ", CreatedAt: 100, UpdatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	if applied, _ := engine.applyAIGrantRule(ctx, rulePayload, &SyncReport{}); !applied {
		t.Fatal("edge-space rule payload was not applied")
	}
	modePayload, err := marshalObject(aiGrantModeObject{ID: "mode/dev-1", DeviceID: " dev-1 ", Mode: "read_only", UpdatedAt: 100})
	if err != nil {
		t.Fatal(err)
	}
	if applied, _ := engine.applyAIGrantMode(ctx, modePayload, &SyncReport{}); !applied {
		t.Fatal("edge-space mode payload was not applied")
	}

	grants, err := guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, ok := grants.Get("dev-1")
	if !ok || grant.DeviceID != "dev-1" {
		t.Fatalf("grant not normalized: %+v %v", grant, ok)
	}
	if _, ok := grants.Get(" dev-1 "); ok {
		t.Fatal("edge-space device ID kept as a separate entry")
	}
	rules := grants.Rules()
	if len(rules) != 1 || rules[0].DeviceID != "dev-1" || rules[0].Path != "/var/log/**" {
		t.Fatalf("rule not normalized: %+v", rules)
	}
	manager, err := guard.NewManager(ctx, device.db)
	if err != nil {
		t.Fatal(err)
	}
	if mode, ok := manager.DeviceMode("dev-1"); !ok || mode != guard.ReadOnly {
		t.Fatalf("mode not normalized: %v %v", mode, ok)
	}
	if _, ok := manager.DeviceMode(" dev-1 "); ok {
		t.Fatal("edge-space device mode kept as a separate entry")
	}

	// An object ID carrying edge spaces mismatches the normalized form and is rejected outright.
	rejected := []ApplyObject{
		{ID: "device/ dev-1 ", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantDeviceObject{ID: "device/ dev-1 ", DeviceID: " dev-1 ", Kinds: []string{"session_exec"}, UpdatedAt: 200})},
		{ID: "mode/ dev-1 ", Kind: KindAIGrant, Payload: applyPayload(t, aiGrantModeObject{ID: "mode/ dev-1 ", DeviceID: " dev-1 ", Mode: "silent", UpdatedAt: 200})},
	}
	for _, object := range rejected {
		entry := engine.applyPlainObject(ctx, object)
		if entry.Result != ApplyResultSkipped || entry.Warning == "" {
			t.Fatalf("object %s = %+v, want skipped with warning", object.ID, entry)
		}
	}
	if grant, _ := grants.Get("dev-1"); len(grant.Kinds) != 1 || grant.Kinds[0] != guard.GrantKindTerminalWrite {
		t.Fatalf("rejected payload mutated the grant: %+v", grant)
	}
	if mode, _ := manager.DeviceMode("dev-1"); mode != guard.ReadOnly {
		t.Fatalf("rejected payload mutated the mode: %v", mode)
	}

	// LWW and tombstones address the normalized entry: a newer update under the trimmed ID updates the same
	// entry (no duplicate), and a trimmed tombstone deletes it.
	updated, err := marshalObject(aiGrantDeviceObject{ID: "device/dev-1", DeviceID: "dev-1", Kinds: []string{"session_exec"}, UpdatedAt: 200})
	if err != nil {
		t.Fatal(err)
	}
	if applied, _ := engine.applyAIGrantDevice(ctx, updated, &SyncReport{}); !applied {
		t.Fatal("newer trimmed grant was not applied")
	}
	grants, err = guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if grant, ok := grants.Get("dev-1"); !ok || !grant.Covers(guard.GrantKindSessionExec) || grant.UpdatedAt != 200 {
		t.Fatalf("grant after LWW update: %+v %v", grant, ok)
	}
	if len(grants.List()) != 1 {
		t.Fatalf("LWW update duplicated the entry: %+v", grants.List())
	}
	if applied, _ := engine.applyAIGrantTombstone(ctx, "device/dev-1", 300, &SyncReport{}); !applied {
		t.Fatal("trimmed tombstone was not applied")
	}
	grants, err = guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grants.Get("dev-1"); ok {
		t.Fatal("tombstone did not delete the normalized entry")
	}
	if applied, _ := engine.applyAIGrantTombstone(ctx, "mode/dev-1", 300, &SyncReport{}); !applied {
		t.Fatal("mode tombstone was not applied")
	}
	manager, err = guard.NewManager(ctx, device.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.DeviceMode("dev-1"); ok {
		t.Fatal("mode tombstone did not delete the normalized entry")
	}
}
