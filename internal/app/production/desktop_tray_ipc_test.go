package production

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestDesktopTrayPreferenceDefaultsToQuitAndPersistsOptIn(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, "", true); err != nil {
		t.Fatal(err)
	}
	get := func() bool {
		t.Helper()
		var preference desktopTrayDTO
		requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "desktop_tray_get", `{}`), &preference)
		return preference.Enabled
	}
	if get() {
		t.Fatal("window close should quit before the user opts into tray behavior")
	}
	var saved desktopTrayDTO
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "desktop_tray_set", `{"enabled":true}`), &saved)
	if !saved.Enabled || !get() {
		t.Fatal("enabled tray preference did not persist")
	}
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "desktop_tray_set", `{"enabled":false}`), &saved)
	if saved.Enabled || get() {
		t.Fatal("disabled tray preference did not persist")
	}
	if err := database.SettingSet(t.Context(), DesktopCloseToTraySettingKey, "invalid"); err != nil {
		t.Fatal(err)
	}
	if get() {
		t.Fatal("invalid tray preference should not hide the window on close")
	}
}

func TestDesktopTrayPreferenceUnavailableInServer(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerStoreCommands(dispatcher, database, nil, "", false); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ command, args string }{
		{"desktop_tray_get", `{}`},
		{"desktop_tray_set", `{"enabled":true}`},
	} {
		response := dispatchStoreTest(dispatcher, request.command, request.args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
			t.Fatalf("%s on server = %+v, want unsupported", request.command, response)
		}
	}
	if enabled, err := DesktopCloseToTray(t.Context(), database); err != nil || enabled {
		t.Fatalf("server changed tray preference: enabled=%v err=%v", enabled, err)
	}
}
