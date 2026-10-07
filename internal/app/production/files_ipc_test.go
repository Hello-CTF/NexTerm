package production

import (
	"bytes"
	"encoding/base64"
	"os"
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
	modules := productionModules(ProductionServices{Store: database, desktop: true})
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
	for _, command := range []string{"files_settings_get", "files_settings_set", "files_save_image"} {
		if !slices.Contains(dispatcher.Commands(), command) {
			t.Fatalf("missing %s in %v", command, dispatcher.Commands())
		}
	}
}

func TestFilesSaveImageIPCDesktopGuard(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	newDispatcher := func(desktop bool) *ipc.Dispatcher {
		modules := productionModules(ProductionServices{Store: database, desktop: desktop})
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
		return dispatcher
	}

	path := t.TempDir() + "/server-side.png"
	response := dispatchStoreTest(newDispatcher(false), "files_save_image",
		`{"path":`+quotedJSON(path)+`,"contentBase64":"aGk="}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server assembly files_save_image = %+v, want unsupported", response)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("server assembly wrote the file: %v", err)
	}

	desktopPath := t.TempDir() + "/desktop.png"
	response = dispatchStoreTest(newDispatcher(true), "files_save_image",
		`{"path":`+quotedJSON(desktopPath)+`,"contentBase64":"aGk="}`)
	var view filesSaveImageView
	requireStoreTestResponse(t, response, &view)
	if view.Bytes != 2 {
		t.Fatalf("desktop assembly save = %+v", view)
	}
}

// TestFilesSettingsSetValidationTable 与前端 FilesCard 的 normalizePublicBaseURL 共享同一张边界用例表,
// 保证 files_settings_set 的前端预检与后端 core.ParsePublicBaseURL 判定一致。
func TestFilesSettingsSetValidationTable(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := registerFilesCommands(dispatcher, database); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		raw       string
		wantOK    bool
		wantValue string
	}{
		{"", true, ""},
		{"https://example.com", true, "https://example.com"},
		{"https://example.com/", true, "https://example.com"},
		{"https://example.com//", true, "https://example.com"},
		{"https://example.com/nexterm/", true, "https://example.com/nexterm"},
		{"https://example.com/nexterm//", true, "https://example.com/nexterm"},
		{"http://example.com", true, "http://example.com"},
		{"ftp://example.com", false, ""},
		{"example.com", false, ""},
		{"https://", false, ""},
		{"https://user:pass@example.com", false, ""},
		{"https://@example.com", false, ""},
		{"https://example.com/?q=1", false, ""},
		{"https://example.com/?", false, ""},
		{"https://example.com/#frag", false, ""},
		{"https://example.com/%zz", false, ""},
	}
	for _, tc := range cases {
		response := dispatchStoreTest(dispatcher, "files_settings_set", `{"publicBaseURL":`+quotedJSON(tc.raw)+`}`)
		if tc.wantOK {
			var view filesSettingsView
			requireStoreTestResponse(t, response, &view)
			if view.PublicBaseURL != tc.wantValue {
				t.Fatalf("%q -> %q, want %q", tc.raw, view.PublicBaseURL, tc.wantValue)
			}
			continue
		}
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%q = %+v, want bad_param", tc.raw, response)
		}
	}
}

func TestFilesSaveImageIPCWritesLocalFile(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	if err := registerFilesImageCommands(dispatcher, true); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/pasted image.png"
	payload := []byte("\x89PNG\r\n\x1a\n fake image bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)

	response := dispatchStoreTest(dispatcher, "files_save_image",
		`{"path":`+quotedJSON(path)+`,"contentBase64":`+quotedJSON(encoded)+`}`)
	var view filesSaveImageView
	requireStoreTestResponse(t, response, &view)
	if view.Path != path || view.Bytes != int64(len(payload)) {
		t.Fatalf("save view = %+v", view)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatalf("written bytes = %v, want %v", written, payload)
	}

	overwrite := []byte("second paste")
	response = dispatchStoreTest(dispatcher, "files_save_image",
		`{"path":`+quotedJSON(path)+`,"contentBase64":`+quotedJSON(base64.StdEncoding.EncodeToString(overwrite))+`}`)
	requireStoreTestResponse(t, response, &view)
	written, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, overwrite) {
		t.Fatalf("overwritten bytes = %q, want %q", written, overwrite)
	}
}

func TestFilesSaveImageIPCValidation(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	if err := registerFilesImageCommands(dispatcher, true); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir() + "/never-written.png"
	oversized := base64.StdEncoding.EncodeToString(make([]byte, filesImageSaveMaxBytes+1))

	for name, args := range map[string]string{
		"empty path":     `{"path":"","contentBase64":"aGk="}`,
		"empty content":  `{"path":` + quotedJSON(target) + `,"contentBase64":""}`,
		"bad base64":     `{"path":` + quotedJSON(target) + `,"contentBase64":"!!!not-base64!!!"}`,
		"oversized body": `{"path":` + quotedJSON(target) + `,"contentBase64":` + quotedJSON(oversized) + `}`,
	} {
		response := dispatchStoreTest(dispatcher, "files_save_image", args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%s = %+v, want bad_param", name, response)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("rejected writes created the target file: %v", err)
	}
}

func quotedJSON(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
