package production

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestProductionHostKeyAcceptMirrorFailureCompensatesLedger(t *testing.T) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "mirror-failure-owner-a")
	database := newKnownHostOwnerTestDB(t)
	keys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	first := knownHostOwnerTestKey("compensate.test", "SHA256:first", "first-key")
	if err := keys.PutHostKey(ownerA, first, false); err != nil {
		t.Fatal(err)
	}
	rowBefore, found, err := database.KnownHostGet(ownerA, first.Host, int32(first.Port), first.KeyType)
	if err != nil || !found {
		t.Fatalf("seeded ledger row = %+v, %v, %v", rowBefore, found, err)
	}
	mirrorPath := breakKnownHostMirror(t, keys, ownerA)

	inserted := knownHostOwnerTestKey("compensate-insert.test", "SHA256:inserted", "inserted-key")
	if err := keys.PutHostKey(ownerA, inserted, false); err == nil {
		t.Fatal("mirror write failure unexpectedly accepted a new host key")
	}
	if _, found, err := database.KnownHostGet(ownerA, inserted.Host, int32(inserted.Port), inserted.KeyType); err != nil || found {
		t.Fatalf("failed mirror write left an uncompensated ledger row: found=%v err=%v", found, err)
	}

	replacement := knownHostOwnerTestKey("compensate.test", "SHA256:replacement", "replacement-key")
	if err := keys.PutHostKey(ownerA, replacement, true); err == nil {
		t.Fatal("mirror write failure unexpectedly replaced a host key")
	}
	rowAfter, found, err := database.KnownHostGet(ownerA, first.Host, int32(first.Port), first.KeyType)
	if err != nil || !found {
		t.Fatalf("ledger row vanished after failed replacement: %v, %v", found, err)
	}
	if rowAfter.ID != rowBefore.ID || rowAfter.Fingerprint != rowBefore.Fingerprint || rowAfter.AddedAt != rowBefore.AddedAt {
		t.Fatalf("failed mirror replacement changed the ledger: before=%+v after=%+v", rowBefore, rowAfter)
	}
	if info, err := os.Stat(mirrorPath); err != nil || !info.IsDir() {
		t.Fatalf("broken mirror path changed: %v, %v", info, err)
	}
}

func TestProductionHostKeyRemoveMirrorFailureRestoresLedger(t *testing.T) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "mirror-remove-owner-a")
	database := newKnownHostOwnerTestDB(t)
	keys, err := newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
	if err != nil {
		t.Fatal(err)
	}
	key := knownHostOwnerTestKey("mirror-remove.test", "SHA256:remove", "remove-key")
	if err := keys.PutHostKey(ownerA, key, false); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, key.Host, int32(key.Port), key.KeyType)
	if err != nil || !found {
		t.Fatalf("seeded ledger row = %+v, %v, %v", row, found, err)
	}
	breakKnownHostMirror(t, keys, ownerA)
	if err := keys.Remove(ownerA, row.ID); err == nil {
		t.Fatal("mirror rewrite failure unexpectedly removed the host key")
	}
	restored, found, err := database.KnownHostGet(ownerA, key.Host, int32(key.Port), key.KeyType)
	if err != nil || !found {
		t.Fatalf("failed mirror rewrite lost the ledger row: %v, %v", found, err)
	}
	if restored != row {
		t.Fatalf("restored ledger row = %+v, want %+v", restored, row)
	}
	var tombstones int
	if err := database.DB().QueryRowContext(ctx,
		"SELECT count(*) FROM sync_tombstone WHERE id = ? AND owner_id = ?", row.ID, ipc.OwnerID(ownerA)).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("failed mirror rewrite left %d tombstones, want 0", tombstones)
	}
}

func TestProductionHostKeyInterruptedAcceptHealsOnConnect(t *testing.T) {
	server := newHostKeyTestServer(t)
	signer := *server.signer.Load()
	publicKey := signer.PublicKey()
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "interrupted-owner-a")
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	if _, err := newProductionHostKeyStore(ctx, path, database, false); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash between the ledger commit and the mirror write: the ledger row exists, the mirror
	// file was never written. Verification must still trust the ledger and heal the mirror.
	if err := database.KnownHostAccept(ownerA, "127.0.0.1", int32(server.port()), publicKey.Type(), gossh.FingerprintSHA256(publicKey)); err != nil {
		t.Fatal(err)
	}
	restarted, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := ssh.Connect(ownerA, ssh.Config{
		Host: "127.0.0.1", Port: server.port(), User: "test", HostKeys: restarted,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	})
	if connection != nil {
		_ = connection.Close()
	}
	if err != nil {
		t.Fatalf("ledger-approved host key was not trusted after restart: %v", err)
	}
	known, err := restarted.HostKeys(ownerA, "127.0.0.1", server.port())
	if err != nil || len(known) != 1 || known[0].Key != base64.StdEncoding.EncodeToString(publicKey.Marshal()) {
		t.Fatalf("healed host keys = %+v, %v", known, err)
	}
}

func TestProductionHostKeyStrayMirrorEntryIsNotTrusted(t *testing.T) {
	server := newHostKeyTestServer(t)
	signer := *server.signer.Load()
	publicKey := signer.PublicKey()
	stray := ssh.HostKey{
		Host: "127.0.0.1", Port: server.port(), KeyType: publicKey.Type(),
		Fingerprint: gossh.FingerprintSHA256(publicKey), Key: base64.StdEncoding.EncodeToString(publicKey.Marshal()),
	}
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "stray-owner-a")
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash between the mirror write and the ledger commit: the mirror holds an entry the ledger
	// never recorded. The stray entry must not silently extend trust.
	keys.mu.Lock()
	_, mirrorPath, err := keys.fileFor(ownerA)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProductionHostKeyFile(mirrorPath, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{stray}}); err != nil {
		t.Fatal(err)
	}
	restarted, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	if known, err := restarted.HostKeys(ownerA, stray.Host, stray.Port); err != nil || len(known) != 0 {
		t.Fatalf("stray mirror entry surfaced as trusted: %+v, %v", known, err)
	}
	connection, err := ssh.Connect(ownerA, ssh.Config{
		Host: stray.Host, Port: stray.Port, User: "test", HostKeys: restarted,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	})
	if connection != nil {
		_ = connection.Close()
	}
	if !errors.Is(err, ssh.ErrHostKeyPending) {
		t.Fatalf("stray mirror entry was trusted without confirmation: %v", err)
	}
	if rows, err := database.KnownHostList(ownerA); err != nil || len(rows) != 0 {
		t.Fatalf("stray mirror entry leaked into the ledger: %+v, %v", rows, err)
	}
	// An explicit acceptance still persists both sides and connects.
	if err := restarted.PutHostKey(ownerA, stray, false); err != nil {
		t.Fatal(err)
	}
	connection, err = ssh.Connect(ownerA, ssh.Config{
		Host: stray.Host, Port: stray.Port, User: "test", HostKeys: restarted,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	})
	if connection != nil {
		_ = connection.Close()
	}
	if err != nil {
		t.Fatalf("explicitly accepted host key did not connect: %v", err)
	}
}

func TestProductionHostKeySharedLegacyMigrationImportsLedger(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	first := knownHostOwnerTestKey("shared-a.test", "SHA256:shared-a", "shared-a-key")
	second := knownHostOwnerTestKey("shared-b.test", "SHA256:shared-b", "shared-b-key")
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{first, second}}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := newProductionHostKeyStore(ctx, path, database, true)
	if err != nil {
		t.Fatal(err)
	}
	if keys.migration.BackupCreated || keys.migration.Copied != 2 || keys.migration.Existing != 0 || keys.migration.Conflicts != 0 || keys.migration.Skipped != 0 {
		t.Fatalf("shared migration report = %+v", keys.migration)
	}
	rows, err := database.KnownHostList(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("shared migration ledger = %+v, %v", rows, err)
	}
	for _, row := range rows {
		if row.OwnerID != "" {
			t.Fatalf("shared migration wrote owner %q: %+v", row.OwnerID, row)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("shared migration rewrote the mirror file: %v", err)
	}
	if _, err := os.Stat(path + ".pre-owner-migration.bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared migration created an account backup: %v", err)
	}
	if known, err := keys.HostKeys(ctx, first.Host, first.Port); err != nil || len(known) != 1 || known[0] != first {
		t.Fatalf("shared migrated host keys = %+v, %v", known, err)
	}

	reloaded, err := newProductionHostKeyStore(ctx, path, database, true)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.migration.BackupCreated || reloaded.migration.Copied != 0 || reloaded.migration.Existing != 2 || reloaded.migration.Conflicts != 0 {
		t.Fatalf("shared migration reentry report = %+v", reloaded.migration)
	}
	rowsAfter, err := database.KnownHostList(ctx)
	if err != nil || len(rowsAfter) != 2 {
		t.Fatalf("shared migration reentry ledger = %+v, %v", rowsAfter, err)
	}
	for index := range rows {
		if rowsAfter[index].ID != rows[index].ID || rowsAfter[index].AddedAt != rows[index].AddedAt {
			t.Fatalf("shared migration reentry changed ledger identity: before=%+v after=%+v", rows[index], rowsAfter[index])
		}
	}
}

func TestProductionHostKeySharedLegacyMigrationConflictKeepsLedger(t *testing.T) {
	ctx := context.Background()
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	legacy := knownHostOwnerTestKey("shared-conflict.test", "SHA256:legacy", "legacy-key")
	other := knownHostOwnerTestKey("shared-other.test", "SHA256:other", "other-key")
	if err := writeProductionHostKeyFile(path, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{legacy, other}}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostAccept(ctx, legacy.Host, int32(legacy.Port), legacy.KeyType, "SHA256:ledger-wins"); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, found, err := database.KnownHostGet(ctx, legacy.Host, int32(legacy.Port), legacy.KeyType)
	if err != nil || !found {
		t.Fatalf("conflicting ledger row = %+v, %v, %v", ledgerBefore, found, err)
	}
	keys, err := newProductionHostKeyStore(ctx, path, database, true)
	if err != nil {
		t.Fatal(err)
	}
	if keys.migration.Copied != 1 || keys.migration.Existing != 0 || keys.migration.Conflicts != 1 {
		t.Fatalf("conflicting shared migration report = %+v", keys.migration)
	}
	ledgerAfter, found, err := database.KnownHostGet(ctx, legacy.Host, int32(legacy.Port), legacy.KeyType)
	if err != nil || !found {
		t.Fatalf("conflicting ledger row after migration = %+v, %v, %v", ledgerAfter, found, err)
	}
	if ledgerAfter.ID != ledgerBefore.ID || ledgerAfter.Fingerprint != ledgerBefore.Fingerprint || ledgerAfter.AddedAt != ledgerBefore.AddedAt {
		t.Fatalf("conflicting migration changed the ledger: before=%+v after=%+v", ledgerBefore, ledgerAfter)
	}
	if known, err := keys.HostKeys(ctx, legacy.Host, legacy.Port); err != nil || len(known) != 0 {
		t.Fatalf("conflicting mirror entry surfaced as trusted: %+v, %v", known, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("conflicting shared migration rewrote the mirror file: %v", err)
	}
}

func breakKnownHostMirror(t *testing.T, keys *productionHostKeyStore, ctx context.Context) string {
	t.Helper()
	keys.mu.Lock()
	_, mirrorPath, err := keys.fileFor(ctx)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mirrorPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mirrorPath, 0o700); err != nil {
		t.Fatal(err)
	}
	return mirrorPath
}
