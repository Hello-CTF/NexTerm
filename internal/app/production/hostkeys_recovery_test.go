package production

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
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

func TestProductionHostKeyVerificationWithoutMirror(t *testing.T) {
	server := newHostKeyTestServer(t)
	signer := *server.signer.Load()
	publicKey := signer.PublicKey()
	material := base64.StdEncoding.EncodeToString(publicKey.Marshal())
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "no-mirror-owner-a")
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	// The ledger alone holds the complete public key and metadata; the mirror file does not exist.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "127.0.0.1", int32(server.port()), publicKey.Type(), gossh.FingerprintSHA256(publicKey), material); err != nil {
		t.Fatal(err)
	}
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	keys.mu.Lock()
	_, mirrorPath, err := keys.fileFor(ownerA)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Startup reconcile rebuilt the mirror from the ledger alone.
	data, err := os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatalf("startup reconcile did not rebuild the mirror: %v", err)
	}
	var rebuilt productionHostKeyFile
	if err := json.Unmarshal(data, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Keys) != 1 || rebuilt.Keys[0].Key != material {
		t.Fatalf("rebuilt mirror = %+v", rebuilt.Keys)
	}
	if known, err := keys.HostKeys(ownerA, "127.0.0.1", server.port()); err != nil || len(known) != 1 || known[0].Key != material {
		t.Fatalf("ledger-backed host keys = %+v, %v", known, err)
	}
	// Corrupt the mirror: verification must still succeed because it reads only the ledger.
	if err := os.WriteFile(mirrorPath, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection, err := ssh.Connect(ownerA, ssh.Config{
		Host: "127.0.0.1", Port: server.port(), User: "test", HostKeys: keys,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	})
	if connection != nil {
		_ = connection.Close()
	}
	if err != nil {
		t.Fatalf("verification depended on the mirror file: %v", err)
	}
	// Restore a writable mirror, then the explicit acceptance path maintains it for desktop interop.
	if err := os.Remove(mirrorPath); err != nil {
		t.Fatal(err)
	}
	if err := keys.PutHostKey(ownerA, ssh.HostKey{Host: "127.0.0.1", Port: server.port(), KeyType: publicKey.Type(), Fingerprint: gossh.FingerprintSHA256(publicKey), Key: material}, false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	var maintained productionHostKeyFile
	if err := json.Unmarshal(data, &maintained); err != nil {
		t.Fatal(err)
	}
	if len(maintained.Keys) != 1 || maintained.Keys[0].Key != material {
		t.Fatalf("maintained mirror = %+v", maintained.Keys)
	}
}

func TestProductionHostKeyStaleMaterialFailsClosed(t *testing.T) {
	server := newHostKeyTestServer(t)
	signerA := *server.signer.Load()
	publicA := signerA.PublicKey()
	publicB := server.rotateHostKey(t)
	signerB := *server.signer.Load()
	materialA := base64.StdEncoding.EncodeToString(publicA.Marshal())
	materialB := base64.StdEncoding.EncodeToString(publicB.Marshal())
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "stale-owner-a")
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	// A fingerprint rotation left the row with the new fingerprint but the old key material.
	if _, err := database.KnownHostAcceptWithKey(ownerA, "127.0.0.1", int32(server.port()), publicA.Type(), gossh.FingerprintSHA256(publicA), materialA); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "127.0.0.1", int32(server.port()), publicA.Type())
	if err != nil || !found {
		t.Fatalf("seed row = %+v, %v, %v", row, found, err)
	}
	if err := database.GORM().WithContext(ctx).Model(&store.KnownHostRow{}).Where("id = ?", row.ID).
		Update("fingerprint", gossh.FingerprintSHA256(publicB)).Error; err != nil {
		t.Fatal(err)
	}
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	// The stale material is never exposed for exact-match verification.
	if known, err := keys.HostKeys(ownerA, "127.0.0.1", server.port()); err != nil || len(known) != 0 {
		t.Fatalf("stale material surfaced for verification: %+v, %v", known, err)
	}
	// The old key is rejected even though its material is what the ledger stores.
	server.signer.Store(&signerA)
	connection, err := ssh.Connect(ownerA, ssh.Config{
		Host: "127.0.0.1", Port: server.port(), User: "test", HostKeys: keys,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	})
	if connection != nil {
		_ = connection.Close()
	}
	if !errors.Is(err, ssh.ErrHostKeyPending) {
		t.Fatalf("stale key material was trusted: %v", err)
	}
	// The rotated key verifies through the ledger fingerprint and completes the material on accept.
	server.signer.Store(&signerB)
	connection, err = ssh.Connect(ownerA, ssh.Config{
		Host: "127.0.0.1", Port: server.port(), User: "test", HostKeys: keys,
		Auth: ssh.AuthConfig{Method: ssh.AuthPassword, Password: "host-key-test"},
	})
	if connection != nil {
		_ = connection.Close()
	}
	if err != nil {
		t.Fatalf("rotated key was not trusted through its fingerprint: %v", err)
	}
	healed, found, err := database.KnownHostGet(ownerA, "127.0.0.1", int32(server.port()), publicA.Type())
	if err != nil || !found {
		t.Fatalf("healed row = %+v, %v, %v", healed, found, err)
	}
	if healed.Fingerprint != gossh.FingerprintSHA256(publicB) || healed.Key != materialB {
		t.Fatalf("material was not completed on accept: %+v", healed)
	}
	if known, err := keys.HostKeys(ownerA, "127.0.0.1", server.port()); err != nil || len(known) != 1 || known[0].Key != materialB {
		t.Fatalf("healed host keys = %+v, %v", known, err)
	}
}

func TestProductionHostKeyStartupReconcileHealsMirror(t *testing.T) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "reconcile-owner-a")
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	// An interrupted full-key accept: the ledger holds the complete key, the mirror was never written.
	healed := knownHostOwnerTestKey("heal.test", "SHA256:heal", "heal-key")
	if _, err := database.KnownHostAcceptWithKey(ownerA, healed.Host, int32(healed.Port), healed.KeyType, healed.Fingerprint, healed.Key); err != nil {
		t.Fatal(err)
	}
	// A fingerprint-only row has no material and must not be mirrored.
	less := knownHostOwnerTestKey("material-less.test", "SHA256:less", "less-key")
	if err := database.KnownHostAccept(ownerA, less.Host, int32(less.Port), less.KeyType, less.Fingerprint); err != nil {
		t.Fatal(err)
	}
	// A stray mirror-only entry (interrupted revocation) sits in the mirror.
	stray := knownHostOwnerTestKey("stray.test", "SHA256:stray", "stray-key")
	keys.mu.Lock()
	_, mirrorPath, err := keys.fileFor(ownerA)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProductionHostKeyFile(mirrorPath, productionHostKeyFile{Version: 1, Keys: []ssh.HostKey{stray}}); err != nil {
		t.Fatal(err)
	}
	if _, err := newProductionHostKeyStore(ctx, path, database, false); err != nil {
		t.Fatal(err)
	}
	// Startup reconcile wrote the ledger's trusted material into the mirror and removed the leftover.
	data, err := os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	var file productionHostKeyFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Keys) != 1 || file.Keys[0] != healed {
		t.Fatalf("reconciled mirror = %+v, want only the ledger-backed entry", file.Keys)
	}
}

func TestProductionHostKeyReconcileRemovesOrphanMirrorFile(t *testing.T) {
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "orphan-owner-a")
	ownerB := ipc.WithUserID(ctx, "orphan-owner-b")
	database := newKnownHostOwnerTestDB(t)
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	keys, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	keyA := knownHostOwnerTestKey("orphan-a.test", "SHA256:orphan-a", "orphan-a-key")
	keyB := knownHostOwnerTestKey("orphan-b.test", "SHA256:orphan-b", "orphan-b-key")
	if err := keys.PutHostKey(ownerA, keyA, false); err != nil {
		t.Fatal(err)
	}
	if err := keys.PutHostKey(ownerB, keyB, false); err != nil {
		t.Fatal(err)
	}
	// Owner A's last ledger row is deleted while the mirror rewrite never runs (interrupted removal):
	// the mirror file outlives its owner's trust.
	rows, err := database.KnownHostList(ownerA)
	if err != nil || len(rows) != 1 {
		t.Fatalf("owner A rows = %+v, %v", rows, err)
	}
	if err := database.KnownHostRemove(ownerA, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	keys.mu.Lock()
	_, mirrorPathA, err := keys.fileFor(ownerA)
	keys.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newProductionHostKeyStore(ctx, path, database, false)
	if err != nil {
		t.Fatal(err)
	}
	// The orphaned mirror file is removed; the owner with remaining trust keeps theirs.
	if _, err := os.Stat(mirrorPathA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned mirror file survived reconcile: %v", err)
	}
	requireKnownHostOwnerTestKey(t, restarted, ownerB, keyB)
}

func knownHostRowID(t *testing.T, database *store.Store, ctx context.Context, key ssh.HostKey) string {
	t.Helper()
	rows, err := database.KnownHostList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Host == key.Host && int(row.Port) == key.Port && row.KeyType == key.KeyType && row.Fingerprint == key.Fingerprint {
			return row.ID
		}
	}
	t.Fatalf("ledger row for %+v not found", key)
	return ""
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
