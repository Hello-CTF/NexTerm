package production

import (
	"slices"
	"strings"
	"testing"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestFilesSettingsIPCPersistence(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerFilesCommands(dispatcher, database); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "files_settings_get", `{}`)
	if !response.OK {
		t.Fatalf("files_settings_get default: %+v", response)
	}
	var view filesSettingsView
	requireStoreTestResponse(t, response, &view)
	if view.PublicBaseURL != "" {
		t.Fatalf("default public base URL = %q, want empty", view.PublicBaseURL)
	}

	response = dispatchStoreTest(dispatcher, "files_settings_set", `{"publicBaseURL":"https://example.com/nexterm/"}`)
	requireStoreTestResponse(t, response, &view)
	if view.PublicBaseURL != "https://example.com/nexterm" {
		t.Fatalf("normalized public base URL = %q", view.PublicBaseURL)
	}
	stored, ok, err := database.SettingGet(ctx, core.FilePublicBaseURLSettingKey)
	if err != nil || !ok || stored != "https://example.com/nexterm" {
		t.Fatalf("persisted setting = %q ok=%v err=%v", stored, ok, err)
	}

	response = dispatchStoreTest(dispatcher, "files_settings_get", `{}`)
	requireStoreTestResponse(t, response, &view)
	if view.PublicBaseURL != "https://example.com/nexterm" {
		t.Fatalf("reloaded public base URL = %q", view.PublicBaseURL)
	}

	for _, invalid := range []string{
		"https://user:pass@example.com",
		"https://example.com/?q=1",
		"https://example.com/#frag",
		"ftp://example.com",
		"example.com",
		"https://",
	} {
		response = dispatchStoreTest(dispatcher, "files_settings_set", `{"publicBaseURL":`+quotedJSON(invalid)+`}`)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("files_settings_set %q = %+v, want bad_param", invalid, response)
		}
	}
	stored, _, _ = database.SettingGet(ctx, core.FilePublicBaseURLSettingKey)
	if stored != "https://example.com/nexterm" {
		t.Fatalf("rejected write changed setting to %q", stored)
	}

	response = dispatchStoreTest(dispatcher, "files_settings_set", `{}`)
	requireStoreTestResponse(t, response, &view)
	if view.PublicBaseURL != "" {
		t.Fatalf("cleared public base URL = %q, want empty", view.PublicBaseURL)
	}
}

func TestFilesCommandsWiredIntoStoreModule(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	modules := productionModules(ProductionServices{Store: database})
	var storeModule *Module
	for index := range modules {
		if modules[index].Name == "store" {
			storeModule = &modules[index]
		}
	}
	if storeModule == nil || storeModule.RegisterCommands == nil {
		t.Fatal("store module missing")
	}
	dispatcher := ipc.NewDispatcher()
	if err := storeModule.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"files_settings_get", "files_settings_set"} {
		if !slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("missing %s in %v", command, dispatcher.Commands())
		}
	}
}

func quotedJSON(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
