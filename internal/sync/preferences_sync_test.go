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
