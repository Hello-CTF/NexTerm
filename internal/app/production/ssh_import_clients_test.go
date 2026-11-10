package production

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func sshImportClientFixture(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "..", "testdata", "sshconfig", "clients"}, parts...)...)
}

func sshImportSelectAll(preview sshImportPreviewDTO) string {
	hosts := make([]string, 0, len(preview.Hosts))
	for _, host := range preview.Hosts {
		if host.Action == "add" {
			hosts = append(hosts, `{"id":`+strconvQuote(host.ID)+`,"action":"import"}`)
		}
	}
	return `"hosts":[` + strings.Join(hosts, ",") + `],"keys":[]`
}

func TestSSHImportClientPreviewCarriesGroups(t *testing.T) {
	t.Parallel()
	dispatcher, _, _, _ := sshImportPreviewTestRig(t, true)
	path := sshImportClientFixture("securecrt", "Sessions")
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"securecrt","path":`+strconvQuote(path)+`}}`)
	if preview.Source != "securecrt" || preview.Path != path {
		t.Fatalf("preview source/path = %q %q", preview.Source, preview.Path)
	}
	web := previewHost(t, preview, "web01")
	if web.Group != "Production" || web.Port != 2222 || web.Username != "deploy" || web.Action != "add" {
		t.Fatalf("web01 = %+v", web)
	}
	if bastion := previewHost(t, preview, "bastion"); bastion.Group != "" || bastion.AuthMethod != "password" {
		t.Fatalf("bastion = %+v", bastion)
	}
	codes := map[string]bool{}
	for _, diagnostic := range preview.Diagnostics {
		codes[diagnostic.Code] = true
	}
	if !codes["password-not-imported"] || !codes["session-protocol-skipped"] {
		t.Fatalf("diagnostics = %+v", preview.Diagnostics)
	}
}

func TestSSHImportClientApplyCreatesFolderGroups(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dispatcher, database, _, _ := sshImportTestRig(t)
	destination, err := database.GroupCreate(ctx, store.GroupInput{Name: "Imported"})
	if err != nil {
		t.Fatal(err)
	}
	// An existing same-named child of the destination is reused case-insensitively.
	existingWeb, err := database.GroupCreate(ctx, store.GroupInput{ParentID: &destination.ID, Name: "production"})
	if err != nil {
		t.Fatal(err)
	}
	path := sshImportClientFixture("mobaxterm", "MobaXterm.ini")
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"mobaxterm","path":`+strconvQuote(path)+`}}`)
	body := `{"args":{"source":"mobaxterm","path":` + strconvQuote(path) + `,"groupId":` + strconvQuote(destination.ID) + `,` + sshImportSelectAll(preview) + `}}`
	result, response := sshImportApply(t, dispatcher, body)
	if !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	// Production exists; Production/Web, Staging and Staging/Web are created.
	if result.AssetsCreated != 5 || result.GroupsCreated != 3 {
		t.Fatalf("apply result = %+v", result)
	}
	groups, err := database.GroupList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pathOf := map[string]string{}
	var resolve func(id string) string
	resolve = func(id string) string {
		for _, group := range groups {
			if group.ID == id {
				if group.ParentID == nil {
					return group.Name
				}
				return resolve(*group.ParentID) + "/" + group.Name
			}
		}
		return "?"
	}
	for _, group := range groups {
		pathOf[group.ID] = resolve(group.ID)
	}
	rows, err := database.AssetList(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	auth := map[string]string{}
	for _, row := range rows {
		got[row.Name] = pathOf[stringValue(row.GroupID)]
		auth[row.Name] = stringValue(row.AuthKind)
	}
	want := map[string]string{
		"Jump Box":             "Imported",
		"Production/Web/web01": "Imported/production/Web",
		"web02":                "Imported/production/Web",
		"web03":                "Imported/production/Web",
		"Staging/Web/web01":    "Imported/Staging/Web",
	}
	for name, group := range want {
		if got[name] != group {
			t.Errorf("asset %q in group %q, want %q (all %v)", name, got[name], group, got)
		}
	}
	if auth["Jump Box"] != "password" || auth["web02"] != "agent" {
		t.Fatalf("auth kinds = %v", auth)
	}
	var reused bool
	for _, group := range groups {
		if group.ParentID != nil && *group.ParentID == existingWeb.ID && group.Name == "Web" {
			reused = true
		}
	}
	if !reused {
		t.Fatalf("Web was not created under the existing production group: %+v", groups)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "使用密码认证") {
		t.Fatalf("warnings = %q, want the password re-entry note", result.Warnings)
	}
}

func TestSSHImportClientSourcesAreDesktopOnly(t *testing.T) {
	t.Parallel()
	dispatcher, _, _, _ := sshImportPreviewTestRig(t, false)
	for _, source := range []string{"putty", "mobaxterm", "xshell", "securecrt"} {
		for _, command := range []string{"ssh_import_preview", "ssh_import_apply"} {
			body := `{"args":{"source":"` + source + `","path":` + strconvQuote(sshImportClientFixture("mobaxterm", "MobaXterm.ini")) + `,"hosts":[],"keys":[]}}`
			response := dispatchStoreTest(dispatcher, command, body)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
				t.Fatalf("server %s %s = %+v, want unsupported", command, source, response)
			}
		}
	}
}

func TestSSHImportClientSourceErrors(t *testing.T) {
	t.Parallel()
	dispatcher, _, _, _ := sshImportPreviewTestRig(t, true)
	expect := func(body string, code ipc.Code, fragment string) {
		t.Helper()
		response := dispatchStoreTest(dispatcher, "ssh_import_preview", body)
		if response.OK || response.Error == nil || response.Error.Code != code || !strings.Contains(response.Error.Message, fragment) {
			t.Fatalf("preview %s = %+v, want %s containing %q", body, response, code, fragment)
		}
	}
	expect(`{"args":{"source":"xshell"}}`, ipc.CodeBadParam, "Xshell")
	expect(`{"args":{"source":"mobaxterm","path":`+strconvQuote(filepath.Join(t.TempDir(), "missing.ini"))+`}}`, ipc.CodeBadParam, "找不到")
	expect(`{"args":{"source":"securecrt","path":`+strconvQuote(sshImportClientFixture("xshell", "Sessions"))+`}}`, ipc.CodeBadParam, "SecureCRT")
	if runtime.GOOS != "windows" {
		expect(`{"args":{"source":"putty"}}`, ipc.CodeUnsupported, ".reg")
	}
}
