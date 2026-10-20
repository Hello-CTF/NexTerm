package production

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func TestSSHImportRejectsChangedExecutablePreview(t *testing.T) {
	for _, test := range []struct {
		name    string
		initial func(string, string) string
		mutated func(string, string) string
	}{
		{
			name: "proxy jump",
			initial: func(keyPath, _ string) string {
				return "Host jump\n  HostName jump.example.com\n\nHost web\n  HostName web.example.com\n  User deploy\n  IdentityFile " + keyPath + "\n  ProxyJump jump\n"
			},
			mutated: func(keyPath, _ string) string {
				return "Host db\n  HostName db.example.com\n\nHost web\n  HostName web.example.com\n  User deploy\n  IdentityFile " + keyPath + "\n  ProxyJump db\n"
			},
		},
		{
			name: "identity file",
			initial: func(keyPath, _ string) string {
				return "Host web\n  HostName web.example.com\n  User deploy\n  IdentityFile " + keyPath + "\n"
			},
			mutated: func(_, otherKeyPath string) string {
				return "Host web\n  HostName web.example.com\n  User deploy\n  IdentityFile " + otherKeyPath + "\n"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher, database, _, _ := sshImportTestRig(t)
			dir := t.TempDir()
			keyPath, _ := sshImportWriteKeyFile(t, dir, "id_one", "")
			otherKeyPath, _ := sshImportWriteKeyFile(t, dir, "id_two", "")
			configPath := filepath.Join(dir, "config")
			if err := os.WriteFile(configPath, []byte(test.initial(keyPath, otherKeyPath)), 0o600); err != nil {
				t.Fatal(err)
			}
			preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
			web := previewHost(t, preview, "web")
			key := preview.Keys[0]
			if err := os.WriteFile(configPath, []byte(test.mutated(keyPath, otherKeyPath)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, response := sshImportApply(t, dispatcher, sshImportApplyRequestBody(t, sshImportApplyRequest{
				Source: "ssh-config", Path: configPath,
				Hosts: []sshImportItemAction{{ID: web.ID, Action: "import"}},
				Keys:  []sshImportItemAction{{ID: key.ID, Action: "import"}},
			}))
			if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "重新预览") {
				t.Fatalf("changed preview response = %+v, want re-preview error", response)
			}
			assets, err := database.AssetList(t.Context(), false)
			if err != nil || len(assets) != 0 {
				t.Fatalf("changed preview wrote assets = %+v, err=%v", assets, err)
			}
			credentials, err := database.CredentialList(t.Context())
			if err != nil || len(credentials) != 0 {
				t.Fatalf("changed preview wrote credentials = %+v, err=%v", credentials, err)
			}
		})
	}
}

func TestSSHImportRejectsReplacedConflictAsset(t *testing.T) {
	dispatcher, database, _, _ := sshImportTestRig(t)
	oldHost := "old.example.com"
	old, err := database.AssetCreate(t.Context(), store.AssetInput{Kind: "ssh", Name: "web", Host: &oldHost, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte("Host web\n  HostName new.example.com\n  User deploy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	web := previewHost(t, preview, "web")
	if web.Action != "conflict-alias" {
		t.Fatalf("preview action = %q", web.Action)
	}
	if err := database.AssetDelete(t.Context(), old.ID); err != nil {
		t.Fatal(err)
	}
	replacementHost := "replacement.example.com"
	replacement, err := database.AssetCreate(t.Context(), store.AssetInput{Kind: "ssh", Name: "web", Host: &replacementHost, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	_, response := sshImportApply(t, dispatcher, sshImportApplyRequestBody(t, sshImportApplyRequest{
		Source: "ssh-config", Path: configPath, Hosts: []sshImportItemAction{{ID: web.ID, Action: "overwrite"}}, Keys: []sshImportItemAction{},
	}))
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "重新预览") {
		t.Fatalf("replaced conflict response = %+v, want re-preview error", response)
	}
	retained, err := database.AssetGet(t.Context(), replacement.ID)
	if err != nil || retained.Host == nil || *retained.Host != replacementHost {
		t.Fatalf("replacement was overwritten = %+v, err=%v", retained, err)
	}
}

func TestSSHImportRejectsReplacedConflictCredential(t *testing.T) {
	dispatcher, database, _, _ := sshImportTestRig(t)
	oldPEM, _ := sshImportTermiusPEM(t)
	var oldID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"id_ed25519","kind":"private_key","secret":`+strconvQuote(oldPEM)+`,"source":"inline"}}`), &oldID)
	dir := t.TempDir()
	keyPath, _ := sshImportWriteKeyFile(t, dir, "id_ed25519", "")
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte("Host web\n  HostName web.example.com\n  IdentityFile "+keyPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	key := previewKey(t, preview, "id_ed25519")
	if key.Action != "conflict-alias" {
		t.Fatalf("key action = %q", key.Action)
	}
	if err := database.CredentialDelete(t.Context(), oldID["id"]); err != nil {
		t.Fatal(err)
	}
	newPEM, _ := sshImportTermiusPEM(t)
	var newID map[string]string
	requireStoreTestResponse(t, dispatchStoreTest(dispatcher, "vault_set_credential",
		`{"args":{"name":"id_ed25519","kind":"private_key","secret":`+strconvQuote(newPEM)+`,"source":"inline"}}`), &newID)
	_, response := sshImportApply(t, dispatcher, sshImportApplyRequestBody(t, sshImportApplyRequest{
		Source: "ssh-config", Path: configPath,
		Hosts: []sshImportItemAction{}, Keys: []sshImportItemAction{{ID: key.ID, Action: "overwrite"}},
	}))
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Message, "重新预览") {
		t.Fatalf("replaced credential response = %+v, want re-preview error", response)
	}
	retained, err := database.CredentialGetRow(t.Context(), newID["id"])
	if err != nil || retained.ID == oldID["id"] {
		t.Fatalf("replacement credential was overwritten = %+v, err=%v", retained, err)
	}
}

func TestSSHImportSelectionOrderDoesNotChangeBinding(t *testing.T) {
	dispatcher, database, _, _ := sshImportTestRig(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	content := "Host one\n  HostName one.example.com\n  User one\n\nHost two\n  HostName two.example.com\n  User two\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	one := previewHost(t, preview, "one")
	two := previewHost(t, preview, "two")
	result, response := sshImportApply(t, dispatcher, sshImportApplyRequestBody(t, sshImportApplyRequest{
		Source: "ssh-config", Path: configPath,
		Hosts: []sshImportItemAction{{ID: two.ID, Action: "import"}, {ID: one.ID, Action: "import"}}, Keys: []sshImportItemAction{},
	}))
	if !response.OK || result.AssetsCreated != 2 {
		t.Fatalf("reordered apply = %+v, response=%+v", result, response)
	}
	rows, err := database.AssetList(t.Context(), false)
	if err != nil || len(rows) != 2 {
		t.Fatalf("reordered assets = %+v, err=%v", rows, err)
	}
	for _, row := range rows {
		if row.Name == "one" && (row.Host == nil || *row.Host != "one.example.com") {
			t.Fatalf("one binding = %+v", row)
		}
		if row.Name == "two" && (row.Host == nil || *row.Host != "two.example.com") {
			t.Fatalf("two binding = %+v", row)
		}
	}
}

func TestSSHImportConflictVersionCheckedInsideTransaction(t *testing.T) {
	_, database, _, planner := sshImportTestRig(t)
	oldHost := "old.example.com"
	old, err := database.AssetCreate(t.Context(), store.AssetInput{Kind: "ssh", Name: "web", Host: &oldHost, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte("Host web\n  HostName new.example.com\n  User deploy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := planner.derive(t.Context(), "ssh-config", configPath, false, true)
	if err != nil {
		t.Fatal(err)
	}
	hostIDs, _ := sshImportPreviewIDs(plan.preview)
	if _, err := database.DB().Exec(`UPDATE asset SET updated_at = updated_at + 1 WHERE id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	result := sshImportApplyDTO{Warnings: []string{}}
	input := sshImportApplyRequest{Source: "ssh-config", Path: configPath,
		Hosts: []sshImportItemAction{{ID: hostIDs[0], Action: "overwrite"}}, Keys: []sshImportItemAction{}}
	err = database.GORM().WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		return planner.applyWrites(t.Context(), tx, plan, input, &result)
	})
	if err == nil || !strings.Contains(err.Error(), "重新预览") {
		t.Fatalf("transaction version check error = %v, want re-preview", err)
	}
	retained, err := database.AssetGet(t.Context(), old.ID)
	if err != nil || retained.Host == nil || *retained.Host != oldHost {
		t.Fatalf("version-changed asset was overwritten = %+v, err=%v", retained, err)
	}
}
