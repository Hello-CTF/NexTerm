package production

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionHostKeyIndependentStoreLifecycle(t *testing.T) {
	database := newKnownHostOwnerTestDB(t)
	for _, test := range []struct {
		name string
		run  func(*testing.T, *store.Store)
	}{
		{name: "TestProductionHostKeyAccountAcceptReplaceRevokeIsolation", run: testProductionHostKeyAccountAcceptReplaceRevokeIsolation},
		{name: "TestProductionHostKeySSHVerificationIsolatedByAccount", run: testProductionHostKeySSHVerificationIsolatedByAccount},
		{name: "TestProductionHostKeyConcurrentOwners", run: testProductionHostKeyConcurrentOwners},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, database)
		})
	}
}

func testProductionHostKeyAccountAcceptReplaceRevokeIsolation(t *testing.T, database *store.Store) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "owner/a")
	ownerB := ipc.WithUserID(ctx, "owner-b")
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	firstA := knownHostOwnerTestKey("shared.test", "SHA256:a-first", "key-a-first")
	replacementA := knownHostOwnerTestKey("shared.test", "SHA256:a-replacement", "key-a-replacement")
	keyB := knownHostOwnerTestKey("shared.test", "SHA256:b", "key-b")
	if err := keys.PutHostKey(ownerA, firstA, false); err != nil {
		t.Fatal(err)
	}
	if err := keys.PutHostKey(ownerB, keyB, false); err != nil {
		t.Fatalf("same endpoint with a different owner key conflicted: %v", err)
	}
	requireKnownHostOwnerTestKey(t, keys, ownerA, firstA)
	requireKnownHostOwnerTestKey(t, keys, ownerB, keyB)

	entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("account host key files = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.Contains(entry.Name(), "owner") {
			t.Fatalf("owner ID leaked into account namespace entry %q", entry.Name())
		}
		if runtime.GOOS != "windows" {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("account host key file mode = %o", info.Mode().Perm())
			}
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), "known_hosts"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("account host key directory mode = %o", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("account mode created the shared trust file: %v", err)
	}

	if err := keys.PutHostKey(ownerA, replacementA, true); err != nil {
		t.Fatal(err)
	}
	requireKnownHostOwnerTestKey(t, keys, ownerA, replacementA)
	requireKnownHostOwnerTestKey(t, keys, ownerB, keyB)
	rowB, found, err := database.KnownHostGet(ownerB, keyB.Host, int32(keyB.Port), keyB.KeyType)
	if err != nil || !found {
		t.Fatalf("owner B ledger = %+v, %v, %v", rowB, found, err)
	}
	if err := keys.Remove(ownerB, rowB.ID); err != nil {
		t.Fatal(err)
	}
	requireKnownHostOwnerTestEmpty(t, keys, ownerB, keyB)
	requireKnownHostOwnerTestKey(t, keys, ownerA, replacementA)

	if err := keys.PutHostKey(ownerB, replacementA, false); err != nil {
		t.Fatalf("owners could not independently trust the same key: %v", err)
	}
	rowA, found, err := database.KnownHostGet(ownerA, replacementA.Host, int32(replacementA.Port), replacementA.KeyType)
	if err != nil || !found {
		t.Fatalf("owner A ledger = %+v, %v, %v", rowA, found, err)
	}
	if err := keys.Remove(ownerA, rowA.ID); err != nil {
		t.Fatal(err)
	}
	requireKnownHostOwnerTestEmpty(t, keys, ownerA, replacementA)
	requireKnownHostOwnerTestKey(t, keys, ownerB, replacementA)
}

func testProductionHostKeySSHVerificationIsolatedByAccount(t *testing.T, database *store.Store) {
	server := newHostKeyTestServer(t)
	firstSigner := *server.signer.Load()
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "ssh-owner-a")
	ownerB := ipc.WithUserID(ctx, "ssh-owner-b")
	keys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	baseConfig := ssh.Config{
		Host: "127.0.0.1", Port: server.port(), User: "test", HostKeys: keys,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	}
	connect := func(ctx context.Context, autoAccept bool) error {
		config := baseConfig
		config.AutoAcceptUnknown = autoAccept
		connection, err := ssh.Connect(ctx, config)
		if connection != nil {
			_ = connection.Close()
		}
		return err
	}
	if err := connect(ownerA, true); err != nil {
		t.Fatalf("owner A could not accept the first host key: %v", err)
	}
	server.rotateHostKey(t)
	secondSigner := *server.signer.Load()
	if err := connect(ownerB, true); err != nil {
		t.Fatalf("owner B could not independently accept the rotated host key: %v", err)
	}
	if err := connect(ownerA, false); !errors.Is(err, ssh.ErrHostKeyChanged) {
		t.Fatalf("owner B's key replaced owner A's trust: %v", err)
	}
	server.signer.Store(&firstSigner)
	if err := connect(ownerA, false); err != nil {
		t.Fatalf("owner A's original host key was not retained: %v", err)
	}
	if err := connect(ownerB, false); !errors.Is(err, ssh.ErrHostKeyChanged) {
		t.Fatalf("owner A's key replaced owner B's trust: %v", err)
	}
	server.signer.Store(&secondSigner)
	if err := connect(ownerB, false); err != nil {
		t.Fatalf("owner B's rotated host key was not retained: %v", err)
	}
}

func TestProductionHostKeyAccountRollbackPreservesOtherOwner(t *testing.T) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "owner-a")
	ownerB := ipc.WithUserID(ctx, "owner-b")
	database := newKnownHostOwnerTestDB(t)
	keys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	originalA := knownHostOwnerTestKey("rollback.test", "SHA256:original-a", "original-a-key")
	replacementA := knownHostOwnerTestKey("rollback.test", "SHA256:replacement-a", "replacement-a-key")
	keyB := knownHostOwnerTestKey("rollback.test", "SHA256:key-b", "b-key")
	if err := keys.PutHostKey(ownerA, originalA, false); err != nil {
		t.Fatal(err)
	}
	if err := keys.PutHostKey(ownerB, keyB, false); err != nil {
		t.Fatal(err)
	}
	_, fileBefore := knownHostOwnerTestFile(t, keys, ownerA)
	if _, err := database.DB().ExecContext(ctx, `CREATE TRIGGER fail_owner_a_host_key_update BEFORE UPDATE ON known_host WHEN NEW.owner_id = 'owner-a' BEGIN SELECT RAISE(ABORT, 'forced update failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(ctx, `CREATE TRIGGER fail_owner_a_host_key_delete BEFORE DELETE ON known_host WHEN OLD.owner_id = 'owner-a' BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := keys.PutHostKey(ownerA, replacementA, true); err == nil {
		t.Fatal("forced ledger update failure unexpectedly replaced owner A trust")
	}
	requireKnownHostOwnerTestKey(t, keys, ownerA, originalA)
	requireKnownHostOwnerTestKey(t, keys, ownerB, keyB)
	_, fileAfterReplace := knownHostOwnerTestFile(t, keys, ownerA)
	if !bytes.Equal(fileAfterReplace, fileBefore) {
		t.Fatal("failed account replacement did not restore the original file")
	}
	rowA, found, err := database.KnownHostGet(ownerA, originalA.Host, int32(originalA.Port), originalA.KeyType)
	if err != nil || !found {
		t.Fatalf("owner A ledger = %+v, %v, %v", rowA, found, err)
	}
	if err := keys.Remove(ownerA, rowA.ID); err == nil {
		t.Fatal("forced ledger delete failure unexpectedly revoked owner A trust")
	}
	requireKnownHostOwnerTestKey(t, keys, ownerA, originalA)
	requireKnownHostOwnerTestKey(t, keys, ownerB, keyB)
	_, fileAfterRemove := knownHostOwnerTestFile(t, keys, ownerA)
	if !bytes.Equal(fileAfterRemove, fileBefore) {
		t.Fatal("failed account removal did not restore the original file")
	}
	if err := keys.PutHostKey(ownerB, originalA, true); err != nil {
		t.Fatalf("owner A rollback blocked owner B: %v", err)
	}
	requireKnownHostOwnerTestKey(t, keys, ownerB, originalA)
	requireKnownHostOwnerTestKey(t, keys, ownerA, originalA)
}

func TestProductionHostKeyAccountMissingOwnerFailsClosed(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	legacy := knownHostOwnerTestKey("legacy.test", "SHA256:legacy", "legacy-key")
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{legacy}}); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostAccept(ctx, legacy.Host, int32(legacy.Port), legacy.KeyType, legacy.Fingerprint); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	if keys.migration.Skipped != 1 {
		t.Fatalf("migration without an active superadmin = %+v", keys.migration)
	}
	if _, err := keys.HostKeys(ctx, legacy.Host, legacy.Port); err == nil {
		t.Fatal("identity-less account read used shared trust")
	} else {
		var ipcErr *ipc.Error
		if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeForbidden {
			t.Fatalf("identity-less account read error = %v", err)
		}
	}
	if _, err := keys.ApprovedHostKeys(ctx, legacy.Host, legacy.Port); err == nil {
		t.Fatal("identity-less account approval read did not fail closed")
	}
	if err := keys.PutHostKey(ctx, legacy, false); err == nil {
		t.Fatal("identity-less account acceptance did not fail closed")
	}
	if err := keys.Remove(ctx, "legacy-id"); err == nil {
		t.Fatal("identity-less account removal did not fail closed")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("identity-less operations changed the legacy file: %v", err)
	}
	rows, err := database.KnownHostListForMigration(ctx)
	if err != nil || len(rows) != 1 || rows[0].OwnerID != "" {
		t.Fatalf("identity-less operations changed the ledger: %+v, %v", rows, err)
	}
}

func TestProductionHostKeyCompositionModes(t *testing.T) {
	for _, test := range []struct {
		name    string
		desktop bool
		open    bool
		shared  bool
	}{
		{name: "desktop", desktop: true, shared: true},
		{name: "explicit open", open: true, shared: true},
		{name: "server account"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.open {
				ctx = owner.WithOpenAccess(ctx)
			}
			dataDir := t.TempDir()
			production, err := NewProduction(ctx, ProductionConfig{
				Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
				DataDir: dataDir,
				Desktop: test.desktop,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := production.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
			keys := production.Services.hostKeys
			if keys == nil || keys.shared != test.shared {
				t.Fatalf("host key shared mode = %+v, want %v", keys, test.shared)
			}
			key := knownHostOwnerTestKey("mode.test", "SHA256:mode", "mode-key")
			err = keys.PutHostKey(context.Background(), key, false)
			if test.shared {
				if err != nil {
					t.Fatal(err)
				}
				requireKnownHostOwnerTestKey(t, keys, context.Background(), key)
				if _, err := os.Stat(filepath.Join(dataDir, "known_hosts.json")); err != nil {
					t.Fatalf("shared mode did not use the legacy path: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dataDir, "known_hosts")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("shared mode created an account namespace: %v", err)
				}
			} else if err == nil {
				t.Fatal("server account composition accepted an identity-less host key")
			}
		})
	}
}

func TestProductionHostKeyLegacyMigrationOwnershipBackupAndReentry(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	putKnownHostOwnerTestUser(t, database, "disabled-admin", "superadmin", "disabled", 1)
	putKnownHostOwnerTestUser(t, database, "owner-b", "user", "active", 10)
	putKnownHostOwnerTestUser(t, database, "owner-c", "user", "active", 11)
	putKnownHostOwnerTestUser(t, database, "admin-first", "superadmin", "active", 20)
	putKnownHostOwnerTestUser(t, database, "admin-second", "superadmin", "active", 30)
	admin := ipc.WithUserID(ctx, "admin-first")
	otherAdmin := ipc.WithUserID(ctx, "admin-second")
	ownerB := ipc.WithUserID(ctx, "owner-b")
	ownerC := ipc.WithUserID(ctx, "owner-c")
	mapped := knownHostOwnerTestKey("mapped.test", "SHA256:mapped", "mapped-key")
	legacy := knownHostOwnerTestKey("legacy.test", "SHA256:legacy", "legacy-key")
	for _, ownerCtx := range []context.Context{ownerB, ownerC} {
		if err := database.KnownHostAccept(ownerCtx, mapped.Host, int32(mapped.Port), mapped.KeyType, mapped.Fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.KnownHostAccept(ctx, legacy.Host, int32(legacy.Port), legacy.KeyType, legacy.Fingerprint); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{mapped, legacy}}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	requireKnownHostOwnerTestKey(t, keys, ownerB, mapped)
	requireKnownHostOwnerTestKey(t, keys, ownerC, mapped)
	requireKnownHostOwnerTestKey(t, keys, admin, legacy)
	requireKnownHostOwnerTestEmpty(t, keys, admin, mapped)
	requireKnownHostOwnerTestEmpty(t, keys, otherAdmin, legacy)
	backup, err := os.ReadFile(path + ".pre-owner-migration.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("legacy backup differs from the original: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path + ".pre-owner-migration.bak")
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("legacy backup mode = %o", info.Mode().Perm())
		}
	}
	adminPath, adminBefore := knownHostOwnerTestFile(t, keys, admin)
	_, ownerBBefore := knownHostOwnerTestFile(t, keys, ownerB)
	adminLedgerBefore, found, err := database.KnownHostGet(admin, legacy.Host, int32(legacy.Port), legacy.KeyType)
	if err != nil || !found {
		t.Fatalf("migrated admin ledger = %+v, %v, %v", adminLedgerBefore, found, err)
	}
	ownerBLedgerBefore, found, err := database.KnownHostGet(ownerB, mapped.Host, int32(mapped.Port), mapped.KeyType)
	if err != nil || !found {
		t.Fatalf("mapped owner ledger = %+v, %v, %v", ownerBLedgerBefore, found, err)
	}

	reloaded, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	// The one-time marker skips the legacy import entirely: entries revoked since the first import can
	// never be resurrected from the untouched legacy file.
	if reloaded.migration.BackupCreated || reloaded.migration.Copied != 0 || reloaded.migration.Existing != 0 || reloaded.migration.Conflicts != 0 || reloaded.migration.Skipped != 0 {
		t.Fatalf("idempotent migration report = %+v", reloaded.migration)
	}
	adminPathAfter, adminAfter := knownHostOwnerTestFile(t, reloaded, admin)
	if adminPathAfter != adminPath || !bytes.Equal(adminAfter, adminBefore) {
		t.Fatal("migration reentry rewrote the admin trust file")
	}
	_, ownerBAfter := knownHostOwnerTestFile(t, reloaded, ownerB)
	if !bytes.Equal(ownerBAfter, ownerBBefore) {
		t.Fatal("migration reentry rewrote the mapped owner trust file")
	}
	adminLedgerAfter, _, _ := database.KnownHostGet(admin, legacy.Host, int32(legacy.Port), legacy.KeyType)
	ownerBLedgerAfter, _, _ := database.KnownHostGet(ownerB, mapped.Host, int32(mapped.Port), mapped.KeyType)
	if adminLedgerAfter.ID != adminLedgerBefore.ID || adminLedgerAfter.AddedAt != adminLedgerBefore.AddedAt || ownerBLedgerAfter.ID != ownerBLedgerBefore.ID || ownerBLedgerAfter.AddedAt != ownerBLedgerBefore.AddedAt {
		t.Fatalf("migration reentry changed ledger identity or revision: admin before=%+v after=%+v, owner before=%+v after=%+v", adminLedgerBefore, adminLedgerAfter, ownerBLedgerBefore, ownerBLedgerAfter)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("migration changed the original file: %v", err)
	}
}

func TestProductionHostKeyLegacyMigrationReentryCompletesPartialCopy(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	putKnownHostOwnerTestUser(t, database, "admin", "superadmin", "active", 1)
	admin := ipc.WithUserID(ctx, "admin")
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	legacy := knownHostOwnerTestKey("partial.test", "SHA256:partial", "partial-key")
	keys.mu.Lock()
	_, targetPath, err := keys.fileFor(admin)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProductionHostKeyFile(targetPath, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{legacy}}); err != nil {
		t.Fatal(err)
	}
	targetBefore, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(admin, legacy.Host, int32(legacy.Port), legacy.KeyType); err != nil || found {
		t.Fatalf("partial copy unexpectedly has a ledger row: found=%v err=%v", found, err)
	}
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{legacy}}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.migration.Copied != 0 || reloaded.migration.Existing != 1 {
		t.Fatalf("partial copy reentry report = %+v", reloaded.migration)
	}
	requireKnownHostOwnerTestKey(t, reloaded, admin, legacy)
	targetAfter, err := os.ReadFile(targetPath)
	if err != nil || !bytes.Equal(targetAfter, targetBefore) {
		t.Fatalf("partial copy reentry rewrote the trust file: %v", err)
	}
}

func TestProductionHostKeyLegacyMigrationConflictPreservesTarget(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	putKnownHostOwnerTestUser(t, database, "admin", "superadmin", "active", 1)
	admin := ipc.WithUserID(ctx, "admin")
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	current := knownHostOwnerTestKey("conflict.test", "SHA256:current", "current-key")
	incoming := knownHostOwnerTestKey("conflict.test", "SHA256:incoming", "incoming-key")
	if err := keys.PutHostKey(admin, current, false); err != nil {
		t.Fatal(err)
	}
	_, targetBefore := knownHostOwnerTestFile(t, keys, admin)
	ledgerBefore, found, err := database.KnownHostGet(admin, current.Host, int32(current.Port), current.KeyType)
	if err != nil || !found {
		t.Fatalf("current ledger = %+v, %v, %v", ledgerBefore, found, err)
	}
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{incoming}}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.migration.Conflicts != 1 || migrated.migration.Copied != 0 {
		t.Fatalf("conflicting migration report = %+v", migrated.migration)
	}
	requireKnownHostOwnerTestKey(t, migrated, admin, current)
	_, targetAfter := knownHostOwnerTestFile(t, migrated, admin)
	if !bytes.Equal(targetAfter, targetBefore) {
		t.Fatal("conflicting migration overwrote the target trust file")
	}
	ledgerAfter, _, _ := database.KnownHostGet(admin, current.Host, int32(current.Port), current.KeyType)
	if ledgerAfter.ID != ledgerBefore.ID || ledgerAfter.Fingerprint != ledgerBefore.Fingerprint || ledgerAfter.AddedAt != ledgerBefore.AddedAt {
		t.Fatalf("conflicting migration changed the target ledger: before=%+v after=%+v", ledgerBefore, ledgerAfter)
	}
	backup, err := os.ReadFile(path + ".pre-owner-migration.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("conflicting migration backup differs from the original: %v", err)
	}
	reentered, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	// The one-time marker skips the legacy import on later startups; the conflicting entry stays out.
	if reentered.migration.Conflicts != 0 || reentered.migration.Copied != 0 || reentered.migration.Existing != 0 {
		t.Fatalf("conflicting migration reentry report = %+v", reentered.migration)
	}
	requireKnownHostOwnerTestKey(t, reentered, admin, current)
}

func TestProductionHostKeyLegacyMigrationBackupConflictKeepsOriginal(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	putKnownHostOwnerTestUser(t, database, "admin", "superadmin", "active", 1)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	legacy := knownHostOwnerTestKey("legacy.test", "SHA256:legacy", "legacy-key")
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{legacy}}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := path + ".pre-owner-migration.bak"
	backupOriginal := []byte("existing backup must not be overwritten")
	if err := os.WriteFile(backupPath, backupOriginal, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newProductionHostKeyStore(ctx, path, database, false); err == nil {
		t.Fatal("conflicting backup was overwritten or ignored")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(after, original) {
		t.Fatalf("failed migration changed the original file: %v", readErr)
	}
	backupAfter, readErr := os.ReadFile(backupPath)
	if readErr != nil || !bytes.Equal(backupAfter, backupOriginal) {
		t.Fatalf("failed migration changed the existing backup: %v", readErr)
	}
	adminRows, err := database.KnownHostList(ipc.WithUserID(ctx, "admin"))
	if err != nil || len(adminRows) != 0 {
		t.Fatalf("failed backup check partially imported ledger rows: %+v, %v", adminRows, err)
	}
}

func testProductionHostKeyConcurrentOwners(t *testing.T, database *store.Store) {
	ctx := context.Background()
	keys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	contexts := []context.Context{
		ipc.WithUserID(context.Background(), "background-owner-a"),
		ipc.WithUserID(context.Background(), "background-owner-b"),
	}
	trusted := []ssh.HostKey{
		knownHostOwnerTestKey("race.test", "SHA256:race-a", "race-key-a"),
		knownHostOwnerTestKey("race.test", "SHA256:race-b", "race-key-b"),
	}
	const workers = 8
	const iterations = 20
	errorsCh := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		index := worker % len(contexts)
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				if err := keys.PutHostKey(contexts[index], trusted[index], false); err != nil {
					errorsCh <- fmt.Errorf("put owner %d: %w", index, err)
					return
				}
				actual, err := keys.HostKeys(contexts[index], trusted[index].Host, trusted[index].Port)
				if err != nil || len(actual) != 1 || actual[0] != trusted[index] {
					errorsCh <- fmt.Errorf("read owner %d: keys=%+v err=%v", index, actual, err)
					return
				}
				approved, err := keys.ApprovedHostKeys(contexts[index], trusted[index].Host, trusted[index].Port)
				if err != nil || len(approved) != 1 || approved[0].Fingerprint != trusted[index].Fingerprint {
					errorsCh <- fmt.Errorf("approval owner %d: keys=%+v err=%v", index, approved, err)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
}

func newKnownHostOwnerTestDB(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func knownHostOwnerTestKey(host, fingerprint, material string) ssh.HostKey {
	// The fingerprint parameter is only a human-readable label: the key material is derived
	// deterministically from the material seed and the fingerprint always matches it, mirroring
	// production rows where stored material and fingerprint are consistent.
	seed := sha256.Sum256([]byte(material))
	signer, err := gossh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed[:]))
	if err != nil {
		panic(err)
	}
	publicKey := signer.PublicKey()
	return ssh.HostKey{
		Host: host, Port: 22, KeyType: publicKey.Type(), Fingerprint: gossh.FingerprintSHA256(publicKey),
		Key: base64.StdEncoding.EncodeToString(publicKey.Marshal()),
	}
}

func requireKnownHostOwnerTestKey(t *testing.T, keys *productionHostKeyStore, ctx context.Context, want ssh.HostKey) {
	t.Helper()
	actual, err := keys.HostKeys(ctx, want.Host, want.Port)
	if err != nil || len(actual) != 1 || actual[0] != want {
		t.Fatalf("host keys = %+v, %v, want %+v", actual, err, want)
	}
	approved, err := keys.ApprovedHostKeys(ctx, want.Host, want.Port)
	if err != nil || len(approved) != 1 || approved[0].Fingerprint != want.Fingerprint {
		t.Fatalf("approved host keys = %+v, %v, want fingerprint %q", approved, err, want.Fingerprint)
	}
}

func requireKnownHostOwnerTestEmpty(t *testing.T, keys *productionHostKeyStore, ctx context.Context, key ssh.HostKey) {
	t.Helper()
	actual, err := keys.HostKeys(ctx, key.Host, key.Port)
	if err != nil || len(actual) != 0 {
		t.Fatalf("unexpected host keys = %+v, %v", actual, err)
	}
	approved, err := keys.ApprovedHostKeys(ctx, key.Host, key.Port)
	if err != nil || len(approved) != 0 {
		t.Fatalf("unexpected approved host keys = %+v, %v", approved, err)
	}
}

func knownHostOwnerTestFile(t *testing.T, keys *productionHostKeyStore, ctx context.Context) (string, []byte) {
	t.Helper()
	keys.mu.Lock()
	_, path, err := keys.fileFor(ctx)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

func putKnownHostOwnerTestUser(t *testing.T, database *store.Store, id, role, state string, createdAt int64) {
	t.Helper()
	user := store.AppUser{
		ID: id, Username: "user-" + id, Role: role, State: state, PasswordHash: "test-only",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := database.GORM().WithContext(context.Background()).Create(&user).Error; err != nil {
		t.Fatal(err)
	}
}
