package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fleetinventory "github.com/Hello-CTF/NexTerm/internal/fleet/inventory"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sshconfig"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type inventoryRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f inventoryRunnerFunc) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func newInventoryTestService(t *testing.T, desktop bool) (*inventoryService, *store.Store) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service := newInventoryService(database, nil, desktop, t.TempDir(), withInventoryWatchInterval(20*time.Millisecond))
	t.Cleanup(func() {
		_ = service.Shutdown(context.Background())
		_ = database.Close()
	})
	return service, database
}

func writeInventoryTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func inventoryAssetByName(database *store.Store, ctx context.Context, name string) (store.AssetRow, bool, error) {
	rows, err := database.AssetList(ctx, true)
	if err != nil {
		return store.AssetRow{}, false, err
	}
	// A watch can delete and recreate an asset under the same name (for
	// example when a tick reads a config file mid-rewrite); the live row is
	// the current state, deleted rows are only the fallback.
	var deleted *store.AssetRow
	for index := range rows {
		row := &rows[index]
		if row.Name != name {
			continue
		}
		if row.DeletedAt == nil {
			return *row, true, nil
		}
		if deleted == nil {
			deleted = row
		}
	}
	if deleted != nil {
		return *deleted, true, nil
	}
	return store.AssetRow{}, false, nil
}

func waitForInventory(t *testing.T, condition func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ok, err := condition()
		if err == nil && ok {
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("inventory condition was not met before timeout: %v", lastErr)
}

func TestInventorySSHWatchResyncRemoveIdempotenceAndManualSafety(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	ctx := context.Background()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config")
	includedPath := filepath.Join(dir, "conf.d", "extra.conf")
	writeInventoryTestFile(t, mainPath, "Include "+includedPath+"\nHost alpha\n  HostName 10.0.0.1\n  User deploy\n")
	writeInventoryTestFile(t, includedPath, "Host beta\n  HostName 10.0.0.2\n  IdentityFile "+filepath.Join(dir, "id_ed25519")+"\n")
	manual, err := database.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "manual", Host: stringPointerForInventory("manual.example"), OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.startWatch(ctx, mainPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Watching || status.LastResult == nil || status.LastResult.Created != 2 {
		t.Fatalf("initial watch status = %+v", status)
	}
	alpha, found, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || !found {
		t.Fatalf("alpha = %+v, found=%v, err=%v", alpha, found, err)
	}
	beta, found, err := inventoryAssetByName(database, ctx, "beta")
	if err != nil || !found || beta.KeyPath == nil || !strings.HasSuffix(*beta.KeyPath, "id_ed25519") {
		t.Fatalf("beta = %+v, found=%v, err=%v", beta, found, err)
	}

	writeInventoryTestFile(t, mainPath, "Include "+includedPath+"\nHost alpha\n  HostName 10.0.0.9\n  User deploy\n")
	writeInventoryTestFile(t, includedPath, "# beta removed\n")
	waitForInventory(t, func() (bool, error) {
		currentAlpha, _, err := inventoryAssetByName(database, ctx, "alpha")
		if err != nil || currentAlpha.Host == nil || *currentAlpha.Host != "10.0.0.9" {
			return false, err
		}
		currentBeta, _, err := inventoryAssetByName(database, ctx, "beta")
		return err == nil && currentBeta.DeletedAt != nil, err
	})
	currentAlpha, _, _ := inventoryAssetByName(database, ctx, "alpha")
	if currentAlpha.ID != alpha.ID {
		t.Fatalf("source update replaced rather than updated asset: old=%s new=%s", alpha.ID, currentAlpha.ID)
	}
	result, _, err := service.syncSSH(ctx, mainPath, status.GroupID)
	if err != nil || result.Created != 0 || result.Updated != 0 || result.Removed != 0 {
		t.Fatalf("unchanged resync = %+v, err=%v", result, err)
	}
	manualAfter, err := database.AssetGet(ctx, manual.ID)
	if err != nil || manualAfter.DeletedAt != nil || manualAfter.UpdatedAt != manual.UpdatedAt {
		t.Fatalf("manual asset changed: %+v, err=%v", manualAfter, err)
	}

	if err := os.Remove(mainPath); err != nil {
		t.Fatal(err)
	}
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.DeletedAt != nil, err
	})
	manualAfter, err = database.AssetGet(ctx, manual.ID)
	if err != nil || manualAfter.DeletedAt != nil {
		t.Fatalf("root deletion removed manual asset: %+v, err=%v", manualAfter, err)
	}
	if err := service.stopWatch(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if service.watchStatus(ctx).Watching {
		t.Fatal("watch remained active after stop")
	}
}

func TestInventoryWatchParseFailureKeepsLastSnapshot(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config")
	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, path, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	waitForInventory(t, func() (bool, error) {
		return service.watchStatus(ctx).LastError != "", nil
	})
	alpha, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || alpha.DeletedAt != nil {
		t.Fatalf("parse failure removed last good snapshot: %+v, err=%v", alpha, err)
	}
}

func TestInventoryServerSyncOwnerIsolationAndPathBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	service, database := newInventoryTestService(t, false)
	sshDir := filepath.Join(home, ".ssh")
	pathA := filepath.Join(sshDir, "config-a")
	pathB := filepath.Join(sshDir, "config-b")
	writeInventoryTestFile(t, pathA, "Host shared\n  HostName a.example\n")
	writeInventoryTestFile(t, pathB, "Host shared\n  HostName b.example\n")
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	resultA, err := service.sync(ctxA, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: pathA})
	if err != nil {
		t.Fatal(err)
	}
	resultB, err := service.sync(ctxB, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: pathB})
	if err != nil {
		t.Fatal(err)
	}
	if resultA.GroupID == resultB.GroupID {
		t.Fatal("server owners share a managed group")
	}
	rowA, _, err := inventoryAssetByName(database, ctxA, "shared")
	if err != nil || rowA.OwnerID != "owner-a" || rowA.Host == nil || *rowA.Host != "a.example" {
		t.Fatalf("owner A asset = %+v, err=%v", rowA, err)
	}
	rowB, _, err := inventoryAssetByName(database, ctxB, "shared")
	if err != nil || rowB.OwnerID != "owner-b" || rowB.Host == nil || *rowB.Host != "b.example" {
		t.Fatalf("owner B asset = %+v, err=%v", rowB, err)
	}
	writeInventoryTestFile(t, pathA, "# empty\n")
	if _, err := service.sync(ctxA, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: pathA}); err != nil {
		t.Fatal(err)
	}
	rowA, _, err = inventoryAssetByName(database, ctxA, "shared")
	if err != nil || rowA.DeletedAt == nil {
		t.Fatalf("owner A removal = %+v, err=%v", rowA, err)
	}
	rowB, _, err = inventoryAssetByName(database, ctxB, "shared")
	if err != nil || rowB.DeletedAt != nil {
		t.Fatalf("owner A sync affected owner B: %+v, err=%v", rowB, err)
	}
	outside := filepath.Join(t.TempDir(), "config")
	writeInventoryTestFile(t, outside, "Host outside\n")
	if _, err := service.sync(ctxA, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: outside}); err == nil {
		t.Fatal("server accepted an SSH inventory outside ~/.ssh")
	}
}

func dispatchInventoryTest(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, command string, input any, output any) ipc.Response {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"args": input})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: raw}, ipc.Environment{})
	if output != nil {
		requireStoreTestResponse(t, response, output)
	}
	return response
}

func TestInventoryDockerDiscoveryReadOnlyAndSelectedSyncIPC(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	service.runner = inventoryRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" || strings.Join(args, " ") != "context ls --format {{json .}}" {
			return nil, errors.New("unexpected command")
		}
		return []byte("{\"Name\":\"prod\",\"DockerEndpoint\":\"ssh://ops@prod\",\"Current\":true}\n{\"Name\":\"local\",\"DockerEndpoint\":\"unix:///docker.sock\",\"Current\":false}\n"), nil
	})
	dispatcher := ipc.NewDispatcher()
	if err := service.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	var discovery inventoryDiscoveryDTO
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_discover", inventoryDiscoverRequest{Source: store.InventorySourceDockerContext}, &discovery)
	if len(discovery.Items) != 2 {
		t.Fatalf("discovery = %+v", discovery)
	}
	if rows, err := database.AssetList(ctxA, false); err != nil || len(rows) != 0 {
		t.Fatalf("read-only discovery wrote assets: %+v, err=%v", rows, err)
	}
	keys := []string{"context/prod"}
	var synced inventorySyncDTO
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_sync", inventorySyncRequest{Source: store.InventorySourceDockerContext, Keys: &keys}, &synced)
	if synced.Created != 1 {
		t.Fatalf("selected sync = %+v", synced)
	}
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_sync", inventorySyncRequest{Source: store.InventorySourceDockerContext, Keys: &keys}, &synced)
	if synced.Created != 0 || synced.Updated != 0 || synced.Unchanged != 1 {
		t.Fatalf("idempotent selected sync = %+v", synced)
	}
	dispatchInventoryTest(t, dispatcher, ctxB, "inventory_sync", inventorySyncRequest{Source: store.InventorySourceDockerContext, Keys: &keys}, &synced)
	empty := []string{}
	dispatchInventoryTest(t, dispatcher, ctxA, "inventory_sync", inventorySyncRequest{Source: store.InventorySourceDockerContext, Keys: &empty}, &synced)
	if synced.Removed != 1 {
		t.Fatalf("empty selection removal = %+v", synced)
	}
	if rows, err := database.AssetList(ctxA, false); err != nil || len(rows) != 0 {
		t.Fatalf("owner A live assets = %+v, err=%v", rows, err)
	}
	if rows, err := database.AssetList(ctxB, false); err != nil || len(rows) != 1 || rows[0].OwnerID != "owner-b" {
		t.Fatalf("owner B assets = %+v, err=%v", rows, err)
	}
	stale := []string{"context/missing"}
	response := dispatchInventoryTest(t, dispatcher, ctxB, "inventory_sync", inventorySyncRequest{Source: store.InventorySourceDockerContext, Keys: &stale}, nil)
	if response.OK {
		t.Fatal("stale candidate key was accepted")
	}
}

func TestInventorySSHWatchTruncatedSnapshotPreservesLastGood(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	service.sshLimits = sshconfig.Limits{MaxHosts: 8}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config")
	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, path, ""); err != nil {
		t.Fatal(err)
	}
	before, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || before.DeletedAt != nil {
		t.Fatalf("initial managed asset = %+v, err=%v", before, err)
	}
	var oversized strings.Builder
	for index := 0; index < 9; index++ {
		fmt.Fprintf(&oversized, "Host h%05d\n  HostName 192.0.2.%d\n", index, index%254+1)
	}
	writeInventoryTestFile(t, path, oversized.String())
	waitForInventory(t, func() (bool, error) {
		return strings.Contains(service.watchStatus(ctx).LastError, "快照不完整"), nil
	})
	after, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || after.DeletedAt != nil || after.UpdatedAt != before.UpdatedAt || after.Host == nil || *after.Host != "10.0.0.1" {
		t.Fatalf("truncated snapshot changed last good asset: %+v, err=%v", after, err)
	}
	if rows, err := database.AssetList(ctx, false); err != nil || len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("truncated snapshot reconciled partial hosts: %+v, err=%v", rows, err)
	}

	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.9\n")
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.Host != nil && *current.Host == "10.0.0.9" && service.watchStatus(ctx).LastError == "", err
	})
}

func TestInventoryServerWatchIncludeEscapeKeepsLastGood(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	service, database := newInventoryTestService(t, false)
	sshDir := filepath.Join(home, ".ssh")
	outside := t.TempDir()
	mainPath := filepath.Join(sshDir, "config")
	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.1\n")
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := service.startWatch(ctx, mainPath, ""); err != nil {
		t.Fatal(err)
	}
	writeInventoryTestFile(t, filepath.Join(outside, "secret.conf"), "Host secret\n  HostName secret.example\n")
	writeInventoryTestFile(t, filepath.Join(home, "escape.conf"), "Host escaped\n  HostName escaped.example\n")
	writeInventoryTestFile(t, mainPath, "Include "+filepath.Join(outside, "secret.conf")+"\nInclude ../escape.conf\nHost alpha\n  HostName 10.0.0.1\n")
	waitForInventory(t, func() (bool, error) {
		return strings.Contains(service.watchStatus(ctx).LastError, "快照不完整"), nil
	})
	alpha, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || alpha.DeletedAt != nil || alpha.Host == nil || *alpha.Host != "10.0.0.1" {
		t.Fatalf("escaping include changed last good snapshot: %+v, err=%v", alpha, err)
	}
	if rows, err := database.AssetList(ctx, false); err != nil || len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("escaping include reconciled partial hosts: %+v, err=%v", rows, err)
	}

	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.9\n")
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.Host != nil && *current.Host == "10.0.0.9" && service.watchStatus(ctx).LastError == "", err
	})
}

func TestInventoryDesktopIncludeEscapeStillParsed(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	base := t.TempDir()
	dir := filepath.Join(base, "ssh")
	outside := t.TempDir()
	mainPath := filepath.Join(dir, "config")
	writeInventoryTestFile(t, mainPath, "Include "+filepath.Join(outside, "extra.conf")+"\nInclude ../escape.conf\nHost alpha\n  HostName 10.0.0.1\n")
	writeInventoryTestFile(t, filepath.Join(outside, "extra.conf"), "Host beta\n  HostName 10.0.0.2\n")
	writeInventoryTestFile(t, filepath.Join(base, "escape.conf"), "Host gamma\n  HostName 10.0.0.3\n")
	ctx := context.Background()
	result, err := service.sync(ctx, inventorySyncRequest{Source: store.InventorySourceSSHConfig, Path: mainPath})
	if err != nil {
		t.Fatalf("desktop sync rejected includes: %v", err)
	}
	if result.Created != 3 {
		t.Fatalf("desktop sync = %+v, want 3 created", result)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		row, found, err := inventoryAssetByName(database, ctx, name)
		if err != nil || !found || row.DeletedAt != nil {
			t.Fatalf("desktop asset %s = %+v, found=%v, err=%v", name, row, found, err)
		}
	}
}

func TestInventorySSHWatchIncludeDepthExceededRecovers(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	service.sshLimits = sshconfig.Limits{MaxIncludeDepth: 2}
	ctx := context.Background()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config")
	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, mainPath, ""); err != nil {
		t.Fatal(err)
	}
	before, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || before.DeletedAt != nil {
		t.Fatalf("initial managed asset = %+v, err=%v", before, err)
	}

	writeInventoryTestFile(t, filepath.Join(dir, "a.conf"), "Include b.conf\nHost a\n  HostName 192.0.2.1\n")
	writeInventoryTestFile(t, filepath.Join(dir, "b.conf"), "Include c.conf\nHost b\n  HostName 192.0.2.2\n")
	writeInventoryTestFile(t, filepath.Join(dir, "c.conf"), "Host c\n  HostName 192.0.2.3\n")
	writeInventoryTestFile(t, mainPath, "Include a.conf\nHost alpha\n  HostName 10.0.0.1\n")
	waitForInventory(t, func() (bool, error) {
		return strings.Contains(service.watchStatus(ctx).LastError, "快照不完整"), nil
	})
	after, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || after.DeletedAt != nil || after.UpdatedAt != before.UpdatedAt || after.Host == nil || *after.Host != "10.0.0.1" {
		t.Fatalf("depth-exceeded snapshot changed last good asset: %+v, err=%v", after, err)
	}
	if rows, err := database.AssetList(ctx, false); err != nil || len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("depth-exceeded snapshot reconciled partial hosts: %+v, err=%v", rows, err)
	}

	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.9\n")
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.Host != nil && *current.Host == "10.0.0.9" && service.watchStatus(ctx).LastError == "", err
	})
}

func TestInventorySSHWatchIncludedFileTooLargeRecovers(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	service.sshLimits = sshconfig.Limits{MaxFileBytes: 64}
	ctx := context.Background()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "config")
	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, mainPath, ""); err != nil {
		t.Fatal(err)
	}
	before, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || before.DeletedAt != nil {
		t.Fatalf("initial managed asset = %+v, err=%v", before, err)
	}

	var oversized strings.Builder
	for index := 0; index < 8; index++ {
		fmt.Fprintf(&oversized, "Host h%d\n  HostName 192.0.2.%d\n", index, index+1)
	}
	writeInventoryTestFile(t, filepath.Join(dir, "big.conf"), oversized.String())
	writeInventoryTestFile(t, mainPath, "Include big.conf\nHost alpha\n  HostName 10.0.0.1\n")
	waitForInventory(t, func() (bool, error) {
		return strings.Contains(service.watchStatus(ctx).LastError, "快照不完整"), nil
	})
	after, _, err := inventoryAssetByName(database, ctx, "alpha")
	if err != nil || after.DeletedAt != nil || after.UpdatedAt != before.UpdatedAt || after.Host == nil || *after.Host != "10.0.0.1" {
		t.Fatalf("oversized include changed last good asset: %+v, err=%v", after, err)
	}
	if rows, err := database.AssetList(ctx, false); err != nil || len(rows) != 1 || rows[0].Name != "alpha" {
		t.Fatalf("oversized include reconciled partial hosts: %+v, err=%v", rows, err)
	}

	writeInventoryTestFile(t, mainPath, "Host alpha\n  HostName 10.0.0.9\n")
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.Host != nil && *current.Host == "10.0.0.9" && service.watchStatus(ctx).LastError == "", err
	})
}

// TestInventoryWatchDeleteRecreateKeepsLiveRowReadable covers the
// delete+recreate path a tick can take when it reads a config file
// mid-rewrite: the asset is removed and recreated as a new row, and status
// assertions must observe the live row rather than the deleted one.
func TestInventoryWatchDeleteRecreateKeepsLiveRowReadable(t *testing.T) {
	service, database := newInventoryTestService(t, true)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config")
	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.1\n")
	if _, err := service.startWatch(ctx, path, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.DeletedAt != nil, err
	})
	writeInventoryTestFile(t, path, "Host alpha\n  HostName 10.0.0.9\n")
	waitForInventory(t, func() (bool, error) {
		current, _, err := inventoryAssetByName(database, ctx, "alpha")
		return err == nil && current.DeletedAt == nil && current.Host != nil && *current.Host == "10.0.0.9" && service.watchStatus(ctx).LastError == "", err
	})
}

var _ fleetinventory.CommandRunner = inventoryRunnerFunc(nil)

func stringPointerForInventory(value string) *string { return &value }
