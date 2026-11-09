package production

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A wildcard IdentityFile reaches every host; when the file does not exist OpenSSH skips it, so hosts must keep agent
// auth instead of storing a broken key path.
func sshImportMissingWildcardKeyConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	content := "Host web\n  HostName web.example.com\n\nHost *\n  IdentityFile " + filepath.Join(dir, "id_missing") + "\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestSSHImportWildcardMissingIdentityFileUsesAgent(t *testing.T) {
	t.Parallel()
	dispatcher, database, _, _ := sshImportTestRig(t)
	configPath := sshImportMissingWildcardKeyConfig(t)
	preview := sshImportPreview(t, dispatcher, `{"args":{"source":"ssh-config","path":`+strconvQuote(configPath)+`}}`)
	web := previewHost(t, preview, "web")
	if web.AuthMethod != "" || len(web.IdentityFiles) != 0 {
		t.Fatalf("web preview = %+v, want no identity files and agent auth", web)
	}
	body := `{"args":{"source":"ssh-config","path":` + strconvQuote(configPath) + `,"hosts":[{"id":"` + web.ID + `","action":"import"}],"keys":[]}}`
	if _, response := sshImportApply(t, dispatcher, body); !response.OK {
		t.Fatalf("apply failed: %+v", response.Error)
	}
	row, err := database.AssetGet(t.Context(), sshImportAssetIDByName(t, database, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if row.AuthKind == nil || *row.AuthKind != "agent" || row.KeyPath != nil {
		t.Fatalf("web authKind = %v keyPath = %v, want agent without a key path", row.AuthKind, row.KeyPath)
	}
}

func TestInventoryWildcardMissingIdentityFileUsesAgent(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	ctx := context.Background()
	configPath := sshImportMissingWildcardKeyConfig(t)
	if _, err := service.sync(ctx, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: configPath}); err != nil {
		t.Fatal(err)
	}
	row, found, err := inventoryAssetByName(database, ctx, "web")
	if err != nil || !found {
		t.Fatalf("web = %+v, found=%v, err=%v", row, found, err)
	}
	if row.AuthKind == nil || *row.AuthKind != "agent" || row.KeyPath != nil {
		t.Fatalf("web authKind = %v keyPath = %v, want agent without a key path", row.AuthKind, row.KeyPath)
	}
}
