package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm/clause"
)

func putPreferenceSetting(t *testing.T, device *testDevice, key, value string, updatedAt int64) {
	t.Helper()
	if err := device.db.GORM().WithContext(context.Background()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&model.Setting{Key: key, Value: value, UpdatedAt: updatedAt}).Error; err != nil {
		t.Fatal(err)
	}
}

func readPreferenceSetting(t *testing.T, device *testDevice, key string) (string, int64, bool) {
	t.Helper()
	var value string
	var updatedAt int64
	err := device.db.DB().QueryRowContext(context.Background(), "SELECT value, updated_at FROM setting WHERE key = ?", key).Scan(&value, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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

// The multi-line paste confirmation mode converges between devices: each enum value set on one device reaches the
// other as the account override the terminal reads.
func TestEngineMultilinePastePreferenceConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB} {
		enableDeviceKindOptIn(t, device, userID)
	}
	const key = "terminal.confirmMultilinePaste"
	overrides := func(device *testDevice) string {
		t.Helper()
		values, err := account.NewPreferences(device.db).UserOverrides(context.Background(), userID)
		if err != nil {
			t.Fatal(err)
		}
		return string(values[key])
	}
	round := func(from, to *testDevice) {
		t.Helper()
		syncDevice(t, from, server, "alice", "alice-pw-123")
		syncDevice(t, to, server, "alice", "alice-pw-123")
	}

	putPreferenceSetting(t, deviceA, "prefs.user."+userID+"."+key, `"never"`, 100)
	round(deviceA, deviceB)
	if got := overrides(deviceB); got != `"never"` {
		t.Fatalf("device B mode = %s, want \"never\"", got)
	}

	putPreferenceSetting(t, deviceB, "prefs.user."+userID+"."+key, `"unbracketed"`, 200)
	round(deviceB, deviceA)
	if got := overrides(deviceA); got != `"unbracketed"` {
		t.Fatalf("device A mode = %s, want \"unbracketed\"", got)
	}

	putPreferenceSetting(t, deviceA, "prefs.user."+userID+"."+key, `"always"`, 300)
	round(deviceA, deviceB)
	if got := overrides(deviceB); got != `"always"` {
		t.Fatalf("device B mode = %s, want \"always\"", got)
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
	objects, err = device.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{optInAIPermission: true, optInPreferences: true}, userID)
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

func grantRulesForTest(t *testing.T, ctx context.Context, grants *guard.Grants) []guard.GrantRule {
	t.Helper()
	rules, err := grants.Rules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func grantForTest(t *testing.T, ctx context.Context, grants *guard.Grants, deviceID string) (guard.DeviceGrant, bool) {
	t.Helper()
	grant, ok, err := grants.Get(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return grant, ok
}

func grantListForTest(t *testing.T, ctx context.Context, grants *guard.Grants) []guard.DeviceGrant {
	t.Helper()
	listed, err := grants.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func deviceModeForTest(t *testing.T, ctx context.Context, manager *guard.Manager, deviceID string) (guard.Mode, bool) {
	t.Helper()
	mode, ok, err := manager.DeviceMode(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return mode, ok
}

func syncDeviceWithContext(t *testing.T, ctx context.Context, device *testDevice, server *testSyncServer, username, password string) SyncReport {
	t.Helper()
	report, err := device.engine.Sync(ctx, device.config(server, username, password))
	if err != nil {
		t.Fatalf("sync: %v (warnings=%v)", err, report.Warnings)
	}
	return report
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
	userCtx := WithUserID(context.Background(), userID)
	ctx := userCtx
	syncDevice := func(t *testing.T, device *testDevice, server *testSyncServer, username, password string) SyncReport {
		return syncDeviceWithContext(t, userCtx, device, server, username, password)
	}

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
	if mode, ok := deviceModeForTest(t, ctx, deviceManager(t, deviceB, ctx), "dev-1"); !ok || mode != guard.ReadOnly {
		t.Fatalf("mode must apply on B: %v %v", mode, ok)
	}
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "device/dev-1", KindAIGrant)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "rule/"+rule.ID, KindAIGrant)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "mode/dev-1", KindAIGrant)

	// LWW: A widens the grant; B converges to the newer revision.
	waitForClockToAdvance(t)
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
	waitForClockToAdvance(t)
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
	if _, ok := deviceModeForTest(t, ctx, deviceManager(t, deviceB, ctx), "dev-1"); ok {
		t.Fatal("cleared mode must be gone on B")
	}

	for _, device := range []*testDevice{deviceA, deviceB} {
		if report := syncDeviceWithContext(t, userCtx, device, server, "alice", "alice-pw-123"); report.Pulled+report.Applied+report.Pushed != 0 {
			t.Fatalf("idle sync not quiescent: %+v", report)
		}
	}
}

// aiGrant opt-in gating: with the kind off, no objects or tombstones are collected; with it on, one object per entity plus tombstones.
// (The store opt-in defaults on; the off case here is the explicit opt-out.)
func TestCollectLocalObjectsHonorsAIGrantOptOut(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	userID := ids.New()
	putPreferenceSetting(t, device, guard.DeviceGrantsSettingKey, `{"dev-1":{"deviceId":"dev-1","kinds":["terminal_write"],"permanent":true,"updatedAt":100,"generation":"gen-dev-1"}}`, 100)
	putPreferenceSetting(t, device, guard.GrantRulesSettingKey, `[{"id":"r-1","deviceId":"dev-1","action":"write_file","path":"/var/log/**","createdAt":100,"updatedAt":100,"generation":"gen-r-1"}]`, 100)
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
	objects, err = device.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{optInAIGrant: true}, userID)
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
	for _, rule := range grantRulesForTest(t, ctx, grants) {
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
	grant, ok := grantForTest(t, ctx, grants, "dev-1")
	if !ok || grant.DeviceID != "dev-1" {
		t.Fatalf("grant not normalized: %+v %v", grant, ok)
	}
	if _, ok := grantForTest(t, ctx, grants, " dev-1 "); ok {
		t.Fatal("edge-space device ID kept as a separate entry")
	}
	rules := grantRulesForTest(t, ctx, grants)
	if len(rules) != 1 || rules[0].DeviceID != "dev-1" || rules[0].Path != "/var/log/**" {
		t.Fatalf("rule not normalized: %+v", rules)
	}
	manager, err := guard.NewManager(ctx, device.db)
	if err != nil {
		t.Fatal(err)
	}
	if mode, ok := deviceModeForTest(t, ctx, manager, "dev-1"); !ok || mode != guard.ReadOnly {
		t.Fatalf("mode not normalized: %v %v", mode, ok)
	}
	if _, ok := deviceModeForTest(t, ctx, manager, " dev-1 "); ok {
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
	if grant, _ := grantForTest(t, ctx, grants, "dev-1"); len(grant.Kinds) != 1 || grant.Kinds[0] != guard.GrantKindTerminalWrite {
		t.Fatalf("rejected payload mutated the grant: %+v", grant)
	}
	if mode, _ := deviceModeForTest(t, ctx, manager, "dev-1"); mode != guard.ReadOnly {
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
	if grant, ok := grantForTest(t, ctx, grants, "dev-1"); !ok || !grant.Covers(guard.GrantKindSessionExec) || grant.UpdatedAt != 200 {
		t.Fatalf("grant after LWW update: %+v %v", grant, ok)
	}
	if len(grantListForTest(t, ctx, grants)) != 1 {
		t.Fatalf("LWW update duplicated the entry: %+v", grantListForTest(t, ctx, grants))
	}
	if applied, _ := engine.applyAIGrantTombstone(ctx, "device/dev-1", 300, &SyncReport{}); !applied {
		t.Fatal("trimmed tombstone was not applied")
	}
	grants, err = guard.NewGrants(ctx, device.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grantForTest(t, ctx, grants, "dev-1"); ok {
		t.Fatal("tombstone did not delete the normalized entry")
	}
	if applied, _ := engine.applyAIGrantTombstone(ctx, "mode/dev-1", 300, &SyncReport{}); !applied {
		t.Fatal("mode tombstone was not applied")
	}
	manager, err = guard.NewManager(ctx, device.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deviceModeForTest(t, ctx, manager, "dev-1"); ok {
		t.Fatal("mode tombstone did not delete the normalized entry")
	}
}

// requireGrantRecordPermanence asserts each named record's persisted permanence marker in the raw
// device grants row; a record missing the marker fails, because rewrites must make permanence explicit.
func requireGrantRecordPermanence(t *testing.T, value string, want map[string]bool) {
	t.Helper()
	var records map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &records); err != nil {
		t.Fatal(err)
	}
	for deviceID, permanent := range want {
		item, ok := records[deviceID]
		if !ok {
			t.Fatalf("grant record %s missing from the row: %s", deviceID, value)
		}
		var marker struct {
			Permanent *bool `json:"permanent"`
		}
		if err := json.Unmarshal(item, &marker); err != nil {
			t.Fatal(err)
		}
		if marker.Permanent == nil || *marker.Permanent != permanent {
			t.Fatalf("grant record %s persisted marker = %v, want explicit %v: %s", deviceID, marker.Permanent, permanent, item)
		}
	}
}

// A synced device grant is a long-term authorization: the apply path must bind the guard's explicit
// permanence marker and a fresh generation exactly like guard.Grant writes it locally, keeping the durable
// record permanent across LWW updates, revocations, and re-grants. Whole-row rewrites (apply and tombstone
// alike) keep current bystanders byte-identical, generation included, and write rejected pre-1.3.0 records
// back verbatim: they are neither migrated nor dropped, and never become effective.
func TestApplyAIGrantDeviceBindsExplicitPermanence(t *testing.T) {
	device := newTestDevice(t)
	ctx := context.Background()
	engine := device.engine

	// Bystanders sharing the row: one pre-1.3.0 record without a permanence marker or generation, and one
	// current-format explicit non-permanent control. No whole-row rewrite may change either of them.
	legacyRecord := `{"deviceId":"dev-legacy-a","kinds":["terminal_write"],"updatedAt":50}`
	currentRecord := `{"deviceId":"dev-explicit-false","kinds":["db_query"],"permanent":false,"updatedAt":70,"generation":"gen-explicit-false"}`
	putPreferenceSetting(t, device, guard.DeviceGrantsSettingKey, `{"dev-legacy-a":`+legacyRecord+`,"dev-explicit-false":`+currentRecord+`}`, 50)
	requireBystanders := func() {
		t.Helper()
		value, _, found := readPreferenceSetting(t, device, guard.DeviceGrantsSettingKey)
		if !found {
			t.Fatal("device grants row missing")
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal([]byte(value), &records); err != nil {
			t.Fatal(err)
		}
		if string(records["dev-legacy-a"]) != legacyRecord {
			t.Fatalf("legacy bystander rewritten: %s", records["dev-legacy-a"])
		}
		var current struct {
			Generation string `json:"generation"`
		}
		if err := json.Unmarshal(records["dev-explicit-false"], &current); err != nil || current.Generation != "gen-explicit-false" {
			t.Fatalf("current bystander generation changed: %s", records["dev-explicit-false"])
		}
		requireGrantRecordPermanence(t, value, map[string]bool{"dev-explicit-false": false})
		grants, err := guard.NewGrants(ctx, device.db, nil)
		if err != nil {
			t.Fatal(err)
		}
		if grant, ok := grantForTest(t, ctx, grants, "dev-explicit-false"); !ok || grant.Permanent || !grant.Covers(guard.GrantKindDBQuery) {
			t.Fatalf("guard loads current bystander as %+v %v", grant, ok)
		}
		if grant, ok := grantForTest(t, ctx, grants, "dev-legacy-a"); ok {
			t.Fatalf("legacy bystander became effective: %+v", grant)
		}
		if decision := deviceGrantsEvaluate(t, device, ctx, "dev-legacy-a", "send_keys"); decision.Action == guard.ActionAllow {
			t.Fatalf("legacy bystander authorized a call: %+v", decision)
		}
	}
	requireBystanders()

	applyGrant := func(updatedAt int64, kinds ...string) {
		t.Helper()
		payload, err := marshalObject(aiGrantDeviceObject{ID: "device/dev-1", DeviceID: "dev-1", Kinds: kinds, UpdatedAt: updatedAt})
		if err != nil {
			t.Fatal(err)
		}
		if applied, _ := engine.applyAIGrantDevice(ctx, payload, &SyncReport{}); !applied {
			t.Fatalf("grant payload %d was not applied", updatedAt)
		}
	}
	requirePermanent := func(updatedAt int64) {
		t.Helper()
		value, revision, found := readPreferenceSetting(t, device, guard.DeviceGrantsSettingKey)
		if !found || revision != updatedAt {
			t.Fatalf("device grants row = %q %d %v, want revision %d", value, revision, found, updatedAt)
		}
		requireGrantRecordPermanence(t, value, map[string]bool{"dev-1": true})
		grants, err := guard.NewGrants(ctx, device.db, nil)
		if err != nil {
			t.Fatal(err)
		}
		if grant, ok := grantForTest(t, ctx, grants, "dev-1"); !ok || !grant.Permanent {
			t.Fatalf("guard loads synced grant as %+v %v, want permanent", grant, ok)
		}
	}

	applyGrant(100, "terminal_write")
	requirePermanent(100)
	requireBystanders()
	if decision := deviceGrantsEvaluate(t, device, ctx, "dev-1", "send_keys"); decision.Action != guard.ActionAllow {
		t.Fatalf("synced permanent grant must allow: %+v", decision)
	}

	// A newer LWW revision keeps the explicit permanence marker and leaves the bystanders intact.
	applyGrant(200, "terminal_write", "session_exec")
	requirePermanent(200)
	requireBystanders()

	// Revocation tombstones the grant (another whole-row rewrite); a re-grant resurrects it as
	// explicit-permanent again, and the bystanders are still intact.
	if applied, _ := engine.applyAIGrantTombstone(ctx, "device/dev-1", 300, &SyncReport{}); !applied {
		t.Fatal("grant tombstone was not applied")
	}
	requireBystanders()
	if decision := deviceGrantsEvaluate(t, device, ctx, "dev-1", "send_keys"); decision.Action == guard.ActionAllow {
		t.Fatal("tombstoned grant must not allow")
	}
	applyGrant(400, "terminal_write")
	requirePermanent(400)
	requireBystanders()
}

// Pre-1.3.0 device grant records (no permanence marker or generation) are rejected, not migrated: the
// collector does not push them, the round reports them as a visible warning, the local row stays
// byte-identical, and other devices never receive them. Current-format records in the same row still sync.
func TestEngineAIGrantLegacyRecordIsNotPushed(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceAIGrantOptIn(t, deviceA, userID)
	enableDeviceAIGrantOptIn(t, deviceB, userID)
	ctx := context.Background()

	row := `{"dev-legacy":{"deviceId":"dev-legacy","kinds":["terminal_write"],"updatedAt":100},"dev-current":{"deviceId":"dev-current","kinds":["db_query"],"permanent":true,"updatedAt":100,"generation":"gen-current"}}`
	putPreferenceSetting(t, deviceA, guard.DeviceGrantsSettingKey, row, 100)
	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	warned := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "1.3.0") && strings.Contains(warning, "dev-legacy") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("legacy grant was skipped without a visible warning: %v", report.Warnings)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	if value, _, found := readPreferenceSetting(t, deviceA, guard.DeviceGrantsSettingKey); !found || value != row {
		t.Fatalf("legacy records mutated on A: %q %v", value, found)
	}
	if serverObjectIDs(t, server)["device/dev-legacy"] {
		t.Fatal("legacy grant was pushed to the server")
	}
	assertServerObjectKind(t, server, "alice", "alice-pw-123", "device/dev-current", KindAIGrant)
	grantsB, err := guard.NewGrants(ctx, deviceB.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grantForTest(t, ctx, grantsB, "dev-legacy"); ok {
		t.Fatal("legacy grant reached B")
	}
	if grant, ok := grantForTest(t, ctx, grantsB, "dev-current"); !ok || !grant.Permanent {
		t.Fatalf("current grant on B = %+v %v", grant, ok)
	}
}
