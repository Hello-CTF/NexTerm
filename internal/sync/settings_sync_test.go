package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm/clause"
)

func putDeviceKnownHost(t *testing.T, device *testDevice, id, host string, port int32, keyType, fingerprint string, addedAt int64) {
	t.Helper()
	if err := device.db.GORM().WithContext(context.Background()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"fingerprint", "added_at"}),
	}).Create(&model.KnownHostRow{
		ID: id, Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, AddedAt: addedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func putDeviceAIProfile(t *testing.T, device *testDevice, record aiProfileRecord, updatedAt int64) {
	t.Helper()
	state, _, _, err := device.engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.upsert(record)
	state.ensureActive()
	if err := device.engine.aiProfilesSave(context.Background(), state, updatedAt, ""); err != nil {
		t.Fatal(err)
	}
}

func putDeviceSealedAIProfile(t *testing.T, device *testDevice, id, name, secret string, updatedAt int64) {
	t.Helper()
	envelope, err := device.vault.EncryptSecret(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	putDeviceAIProfile(t, device, aiProfileRecord{
		ID: id, Name: name, BaseURL: "https://ai.example.com", APIKey: envelope, Model: "model-x", Stream: true,
	}, updatedAt)
}

func deviceAIProfileState(t *testing.T, device *testDevice) aiProfilesState {
	t.Helper()
	state, _, _, err := device.engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func deviceKnownHost(t *testing.T, device *testDevice, id string) (store.KnownHostRow, bool) {
	t.Helper()
	row, found, err := device.engine.knownHostByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row, found
}

// serverUserID returns the test account's user ID, for writing per-user device-side opt-in keys.
func serverUserID(t *testing.T, server *testSyncServer, username, password string) string {
	t.Helper()
	user, err := server.accounts.Authenticate(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

// enableDeviceKindOptIn enables known_host/AI profile sync for the given user on the device (opt-in is stored device-side, keyed by user).
// After the M165 engine gating, sync tests touching these two kinds enable them explicitly; the default-on case is covered by TestEngineKindOptInDefaultOnAndDisable.
func enableDeviceKindOptIn(t *testing.T, device *testDevice, userID string) {
	t.Helper()
	if err := device.db.SetKnownHostSyncOptIn(context.Background(), userID, true); err != nil {
		t.Fatal(err)
	}
	if err := device.db.SetAIProfileSyncOptIn(context.Background(), userID, true); err != nil {
		t.Fatal(err)
	}
}

// deleteDeviceKnownHost/deleteDeviceAIProfile simulate the local deletion entry points (later store/app slices wire in): tombstone first, then delete the row.
// User-initiated deletion is a plain tombstone: conflict triple metadata is cleared too, so re-propagation must not carry the loser triple.
func deleteDeviceKnownHost(t *testing.T, device *testDevice, id string, deletedAt int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := device.db.DB().ExecContext(ctx, "DELETE FROM setting WHERE key = ?", knownHostTombstoneMetaKey(id)); err != nil {
		t.Fatal(err)
	}
	if err := device.engine.syncTombstonePut(ctx, id, KindKnownHost, deletedAt); err != nil {
		t.Fatal(err)
	}
	if err := device.db.KnownHostRemove(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func deleteDeviceAIProfile(t *testing.T, device *testDevice, id string, deletedAt int64) {
	t.Helper()
	ctx := context.Background()
	if err := device.engine.syncTombstonePut(ctx, id, KindAIProfile, deletedAt); err != nil {
		t.Fatal(err)
	}
	state := deviceAIProfileState(t, device)
	if state.remove(id) {
		state.ensureActive()
		if err := device.engine.aiProfilesSave(ctx, state, deletedAt, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func requireDeviceQuiescent(t *testing.T, device *testDevice, server *testSyncServer, username, password string) {
	t.Helper()
	if report := syncDevice(t, device, server, username, password); report.Pulled+report.Applied+report.Pushed != 0 {
		t.Fatalf("idle sync not quiescent: %+v", report)
	}
}

// assertServerObjectKind decrypts the server-side ciphertext directly, verifying which kind (object/tombstone) currently occupies the object slot.
func assertServerObjectKind(t *testing.T, server *testSyncServer, username, password, objectID, wantKind string) {
	t.Helper()
	var blob []byte
	if err := server.db.DB().QueryRowContext(context.Background(),
		"SELECT blob FROM user_sync_object WHERE id = ?", objectID).Scan(&blob); err != nil {
		t.Fatalf("server object %s: %v", objectID, err)
	}
	kind, _, err := tryOpenObject(mustDEK(t, server, username, password), blob, objectID)
	if err != nil || kind != wantKind {
		t.Fatalf("server object %s kind=%q err=%v, want %q", objectID, kind, err, wantKind)
	}
}

// Review regression (order one: loser pushes first, winner syncs later): the winner must tombstone the loser and push it away when it syncs,
// the server-side loser object is replaced by the tombstone; after the winner is then deleted, new devices must not revive the loser's old fingerprint.
func TestEngineKnownHostLoserFirstWinnerTombstones(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	loser := newTestDevice(t)
	winner := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, loser, userID)
	enableDeviceKindOptIn(t, winner, userID)

	loserID := ids.New()
	winnerID := ids.New()
	putDeviceKnownHost(t, loser, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:stale", 100)
	putDeviceKnownHost(t, winner, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:fresh", 200)

	syncDevice(t, loser, server, "alice", "alice-pw-123")
	syncDevice(t, winner, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", loserID, KindTombstone)
	if row, found := deviceKnownHost(t, winner, winnerID); !found || row.Fingerprint != "SHA256:fresh" {
		t.Fatalf("winner must keep its row: %+v found=%v", row, found)
	}

	// Loser device syncs again: the tombstone clears its local old row.
	syncDevice(t, loser, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, loser, loserID); found {
		t.Fatal("loser row must be tombstoned on the loser device")
	}
	assertDeviceTombstone(t, loser, loserID, KindKnownHost)

	// After deleting the winner and propagating, new devices must not get any row of the loser (or the winner).
	deleteDeviceKnownHost(t, winner, winnerID, ids.NowMS())
	syncDevice(t, winner, server, "alice", "alice-pw-123")
	syncDevice(t, loser, server, "alice", "alice-pw-123")

	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	rows, err := newcomer.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("newcomer must not resurrect any triple row: %+v err=%v", rows, err)
	}
	requireDeviceQuiescent(t, loser, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, winner, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// Review regression (R2): a local incarnation with the same ID but a different triple must not coexist with a same-ID tombstone.
// After the incarnation yields its original ID (re-ID): the trusted content is preserved on all devices under the new ID, the server-side loser slot is replaced by a tombstone,
// later same-ID tombstones do not mistakenly delete the re-IDed record, and after a second convergence all devices are quiescent.
func TestEngineKnownHostIncarnationReIDConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	rogue := newTestDevice(t)
	ctx := context.Background()
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB, rogue} {
		enableDeviceKindOptIn(t, device, userID)
	}

	sharedID := ids.New()
	winnerID := ids.New()
	putDeviceKnownHost(t, deviceA, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:trusted", 50)
	putDeviceKnownHost(t, deviceA, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, deviceB, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, rogue, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:rogue", 100)

	syncDevice(t, rogue, server, "alice", "alice-pw-123")   // loser object (sharedID, new triple, 100) lands on the server
	syncDevice(t, deviceA, server, "alice", "alice-pw-123") // A: incarnation re-ID, original ID tombstoned and pushed away

	assertServerObjectKind(t, server, "alice", "alice-pw-123", sharedID, KindTombstone)
	if _, found := deviceKnownHost(t, deviceA, sharedID); found {
		t.Fatal("original id must be vacated on A")
	}
	assertDeviceTombstone(t, deviceA, sharedID, KindKnownHost)

	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, rogue, server, "alice", "alice-pw-123")

	// All devices (including late joiners) hold the incarnation's trusted content (new ID) and the winner row, and none holds the original-ID row.
	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	devices := map[string]*testDevice{"A": deviceA, "B": deviceB, "rogue": rogue, "newcomer": newcomer}
	incarnationIDs := map[string]string{}
	for name, device := range devices {
		rows, err := device.db.KnownHostList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var trusted, winner bool
		for _, row := range rows {
			if row.ID == sharedID {
				t.Fatalf("device%s still holds the original id: %+v", name, row)
			}
			if row.Host == "old.example.com" {
				trusted = row.Fingerprint == "SHA256:trusted" && row.AddedAt == 50 && row.KeyType == "ssh-rsa"
				incarnationIDs[name] = row.ID
			}
			if row.ID == winnerID {
				winner = row.Fingerprint == "SHA256:winner"
			}
		}
		if !trusted || !winner || len(rows) != 2 {
			t.Fatalf("device%s rows=%+v, want trusted incarnation + winner only", name, rows)
		}
	}
	if len(incarnationIDs) != 4 {
		t.Fatalf("every device must hold the incarnation: %v", incarnationIDs)
	}

	// A later tombstone with the original ID (higher revision) must not mistakenly delete the re-IDed incarnation.
	tombstonePayload, err := marshalObject(tombstoneObject{TargetKind: KindKnownHost, DeletedAt: ids.NowMS()})
	if err != nil {
		t.Fatal(err)
	}
	report := &SyncReport{}
	applied, _ := deviceA.engine.applyDecryptedObject(ctx, sharedID, KindTombstone, tombstonePayload, report)
	if !applied {
		t.Fatal("same-id tombstone must be absorbed")
	}
	if row, found := deviceKnownHost(t, deviceA, incarnationIDs["A"]); !found || row.Fingerprint != "SHA256:trusted" {
		t.Fatalf("re-IDed incarnation must survive the same-id tombstone: %+v found=%v", row, found)
	}

	// The newer tombstone propagates with A's push; the other devices each pull once: the incarnation survives on all devices.
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	for name, device := range devices {
		if name == "A" {
			continue
		}
		syncDevice(t, device, server, "alice", "alice-pw-123")
		if row, found := deviceKnownHost(t, device, incarnationIDs[name]); !found || row.Fingerprint != "SHA256:trusted" {
			t.Fatalf("device%s re-IDed incarnation must survive the propagated tombstone: %+v found=%v", name, row, found)
		}
	}

	// Second convergence: idle sync is silent on all devices.
	for name, device := range devices {
		if report := syncDevice(t, device, server, "alice", "alice-pw-123"); report.Pulled+report.Applied+report.Pushed != 0 {
			t.Fatalf("device%s not quiescent: %+v", name, report)
		}
	}
}

// Review regression (R3): while other devices still hold the same trusted copy under the original ID, the re-IDed object must win
// the same-addedAt hash duel - the old copy can only take the displaced path keeping the new ID, the original-ID tombstone only clears the old slot, and the trusted record is not lost.
func TestEngineKnownHostReIDBeatsOldCopyHashDuel(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceB := newTestDevice(t)
	deviceA := newTestDevice(t)
	ctx := context.Background()
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceB, userID)
	enableDeviceKindOptIn(t, deviceA, userID)

	sharedID := ids.New()
	winnerID := ids.New()
	// B and A both hold the trusted copy under the original ID and the triple winner row (from an earlier sync).
	putDeviceKnownHost(t, deviceB, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:trusted", 50)
	putDeviceKnownHost(t, deviceB, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, deviceA, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:trusted", 50)
	putDeviceKnownHost(t, deviceA, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	rogue := newTestDevice(t)
	enableDeviceKindOptIn(t, rogue, userID)
	putDeviceKnownHost(t, rogue, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:rogue", 100)

	syncDevice(t, rogue, server, "alice", "alice-pw-123")   // loser object (sharedID, migrated triple, 100) lands on the server
	syncDevice(t, deviceB, server, "alice", "alice-pw-123") // B syncs first: re-ID + original ID tombstoned and pushed away

	// Constructive invariant: B's re-ID payload must beat the original-ID copy at the same revision (the loser ordering is constructively rejected in both hash orders).
	rows, err := deviceB.db.KnownHostList(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("deviceB rows=%+v err=%v", rows, err)
	}
	var reID string
	for _, row := range rows {
		if row.Host == "old.example.com" {
			reID = row.ID
		}
	}
	if reID == "" || reID == sharedID {
		t.Fatalf("deviceB must re-ID the incarnation: %+v", rows)
	}
	oldPayload, err := marshalObject(knownHostObject{ID: sharedID, Host: "old.example.com", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:trusted", AddedAt: 50})
	if err != nil {
		t.Fatal(err)
	}
	newPayload, err := marshalObject(knownHostObject{ID: reID, Host: "old.example.com", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:trusted", AddedAt: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !remoteWins(50, 50, newPayload, oldPayload) {
		t.Fatal("re-ID payload must provably win the equal-revision hash duel against the old copy")
	}

	// A, still holding the original copy, syncs later: it can only displace the re-IDed object (adopting the new ID); the original-ID tombstone is absorbed, trusted content and revision preserved.
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceA, sharedID); found {
		t.Fatal("deviceA must displace the old copy")
	}
	if row, found := deviceKnownHost(t, deviceA, reID); !found || row.Fingerprint != "SHA256:trusted" || row.AddedAt != 50 {
		t.Fatalf("deviceA must adopt the re-IDed record intact: %+v found=%v", row, found)
	}
	assertDeviceTombstone(t, deviceA, sharedID, KindKnownHost)

	// Server slots: the original ID is a tombstone, the new ID is a live object (must not be replaced by a tombstone).
	assertServerObjectKind(t, server, "alice", "alice-pw-123", sharedID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", reID, KindKnownHost)

	// The newcomer likewise gets the trusted content (new ID, original revision).
	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	if row, found := deviceKnownHost(t, newcomer, reID); !found || row.Fingerprint != "SHA256:trusted" || row.AddedAt != 50 {
		t.Fatalf("newcomer must receive the re-IDed record: %+v found=%v", row, found)
	}
	if _, found := deviceKnownHost(t, newcomer, sharedID); found {
		t.Fatal("newcomer must not resurrect the original id")
	}

	// Second sync is silent.
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// incarnationIDOf returns the row ID of the given host on the device (after re-ID, callers assert content rather than the concrete ID).
func incarnationIDOf(t *testing.T, device *testDevice, host string) string {
	t.Helper()
	rows, err := device.db.KnownHostList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Host == host {
			return row.ID
		}
	}
	t.Fatalf("no row for host %s: %+v", host, rows)
	return ""
}

// Review regression (R4): distinguishing conflict tombstones from user deletion tombstones. B holds an old copy (50), A holds a newer incarnation with the same ID (60),
// and the triple winner (200): when A absorbs the conflict tombstone it must safely re-ID and preserve the newer trusted content (not clear it as a user deletion),
// the original ID and the loser's re-ID slot both converge to tombstones, the newest content is preserved on A/B/rogue/new devices, and a second sync is silent.
func TestEngineKnownHostConflictTombstonePreservesNewerIncarnation(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceB := newTestDevice(t)
	deviceA := newTestDevice(t)
	rogue := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceB, deviceA, rogue} {
		enableDeviceKindOptIn(t, device, userID)
	}

	sharedID := ids.New()
	winnerID := ids.New()
	putDeviceKnownHost(t, deviceB, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:older", 50)
	putDeviceKnownHost(t, deviceB, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, deviceA, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:newer", 60)
	putDeviceKnownHost(t, deviceA, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, rogue, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:rogue", 100)

	syncDevice(t, rogue, server, "alice", "alice-pw-123")   // loser object (100) lands on the server
	syncDevice(t, deviceB, server, "alice", "alice-pw-123") // B: old copy re-ID + conflict tombstone (200) + push
	if _, found := deviceKnownHost(t, deviceB, sharedID); found {
		t.Fatal("B must vacate the original id")
	}
	olderReID := incarnationIDOf(t, deviceB, "old.example.com")
	if olderReID == sharedID {
		t.Fatal("B must re-ID under a fresh id")
	}

	syncDevice(t, deviceA, server, "alice", "alice-pw-123") // A: newer incarnation preserved via re-ID, old re-ID slot tombstoned

	if _, found := deviceKnownHost(t, deviceA, sharedID); found {
		t.Fatal("A must vacate the original id")
	}
	if _, found := deviceKnownHost(t, deviceA, olderReID); found {
		t.Fatal("A must tombstone the older re-ID slot")
	}
	newerReID := incarnationIDOf(t, deviceA, "old.example.com")
	if newerReID == sharedID || newerReID == olderReID {
		t.Fatalf("A must preserve the newer incarnation under its own fresh id: %s", newerReID)
	}
	if row, found := deviceKnownHost(t, deviceA, newerReID); !found || row.Fingerprint != "SHA256:newer" || row.AddedAt != 60 {
		t.Fatalf("A must preserve the newer trust content at revision 60: %+v found=%v", row, found)
	}
	assertDeviceTombstone(t, deviceA, sharedID, KindKnownHost)
	assertDeviceTombstone(t, deviceA, olderReID, KindKnownHost)

	// B adopts the newer incarnation and drops the old re-ID; rogue's loser row is cleared by the same-triple conflict tombstone.
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, rogue, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, rogue, sharedID); found {
		t.Fatal("rogue row must be deleted by the same-triple conflict tombstone")
	}
	if row, found := deviceKnownHost(t, deviceB, newerReID); !found || row.Fingerprint != "SHA256:newer" || row.AddedAt != 60 {
		t.Fatalf("B must adopt the newer incarnation: %+v found=%v", row, found)
	}
	if _, found := deviceKnownHost(t, deviceB, olderReID); found {
		t.Fatal("B must drop the older re-ID")
	}

	// The newcomer gets the newest trusted content (60); the server-side original ID and loser re-ID slots are both tombstones, the new ID is the live object.
	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	if row, found := deviceKnownHost(t, newcomer, newerReID); !found || row.Fingerprint != "SHA256:newer" || row.AddedAt != 60 {
		t.Fatalf("newcomer must receive the newer incarnation: %+v found=%v", row, found)
	}
	if _, found := deviceKnownHost(t, newcomer, sharedID); found {
		t.Fatal("newcomer must not resurrect the original id")
	}
	if _, found := deviceKnownHost(t, newcomer, olderReID); found {
		t.Fatal("newcomer must not resurrect the older re-ID")
	}
	assertServerObjectKind(t, server, "alice", "alice-pw-123", sharedID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", olderReID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", newerReID, KindKnownHost)

	// Second sync is silent: no continuous re-pushing, no tombstone flapping.
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, rogue, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// Review regression (order two: winner pushes first, loser syncs later): the loser tombstones itself via the displaced path and adopts the winner;
// after the winner is deleted and a second convergence completes, new devices must not revive the loser either.
func TestEngineKnownHostWinnerFirstLoserDisplaced(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	winner := newTestDevice(t)
	loser := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, winner, userID)
	enableDeviceKindOptIn(t, loser, userID)

	winnerID := ids.New()
	loserID := ids.New()
	putDeviceKnownHost(t, winner, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:fresh", 200)
	putDeviceKnownHost(t, loser, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:stale", 100)

	syncDevice(t, winner, server, "alice", "alice-pw-123")
	syncDevice(t, loser, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", loserID, KindTombstone)
	if _, found := deviceKnownHost(t, loser, loserID); found {
		t.Fatal("loser row must be displaced locally")
	}
	if row, found := deviceKnownHost(t, loser, winnerID); !found || row.Fingerprint != "SHA256:fresh" {
		t.Fatalf("loser must adopt the winner row: %+v found=%v", row, found)
	}

	deleteDeviceKnownHost(t, winner, winnerID, ids.NowMS())
	syncDevice(t, winner, server, "alice", "alice-pw-123")
	syncDevice(t, loser, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, loser, winnerID); found {
		t.Fatal("winner row must be tombstoned on the loser device")
	}

	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	rows, err := newcomer.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("newcomer must not resurrect any triple row: %+v err=%v", rows, err)
	}
	requireDeviceQuiescent(t, winner, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, loser, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

func TestEngineSyncKnownHostsAndAIProfilesConverge(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:sync-fp", 100)
	keyedID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, keyedID, "同步档案", "sync-key-值", 100)
	keylessID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: keylessID, Name: "无密钥档案", Model: "model-y", Stream: true}, 100)

	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if report.Pushed != 3 {
		t.Fatalf("deviceA pushed=%d want 3: %+v", report.Pushed, report)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	row, found := deviceKnownHost(t, deviceB, hostID)
	if !found || row.Host != "git.example.com" || row.Port != 22 || row.KeyType != "ssh-ed25519" ||
		row.Fingerprint != "SHA256:sync-fp" || row.AddedAt != 100 {
		t.Fatalf("deviceB known host=%+v found=%v", row, found)
	}
	state := deviceAIProfileState(t, deviceB)
	keyed, exists := state.find(keyedID)
	if !exists {
		t.Fatal("deviceB missing keyed profile")
	}
	if !strings.HasPrefix(keyed.APIKey, store.SecretEnvelopePrefix) {
		t.Fatalf("deviceB profile key must be re-sealed with local vault: %q", keyed.APIKey)
	}
	key, err := deviceB.vault.DecryptSecret(ctx, keyed.APIKey)
	if err != nil || key != "sync-key-值" {
		t.Fatalf("deviceB key=%q err=%v", key, err)
	}
	if keyed.Name != "同步档案" || keyed.BaseURL != "https://ai.example.com" || keyed.Model != "model-x" || !keyed.Stream {
		t.Fatalf("deviceB keyed profile fields=%+v", keyed)
	}
	keyless, exists := state.find(keylessID)
	if !exists || keyless.APIKey != "" {
		t.Fatalf("deviceB keyless profile=%+v exists=%v", keyless, exists)
	}
	if state.ActiveID == nil {
		t.Fatal("deviceB profiles must keep an active profile")
	}

	// B's new addition flows back to A.
	hostB := ids.New()
	putDeviceKnownHost(t, deviceB, hostB, "db.example.com", 2222, "ssh-rsa", "SHA256:from-b", 200)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if row, found := deviceKnownHost(t, deviceA, hostB); !found || row.Fingerprint != "SHA256:from-b" {
		t.Fatalf("deviceA missing known host from B: %+v found=%v", row, found)
	}

	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

func TestEngineSettingsDeletionConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:gone", 100)
	profileID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, profileID, "待删档案", "gone-key", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("deviceB must hold the known host before deletion")
	}

	deleteDeviceKnownHost(t, deviceA, hostID, ids.NowMS())
	deleteDeviceAIProfile(t, deviceA, profileID, ids.NowMS())
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("known host must be tombstoned on B")
	}
	if state := deviceAIProfileState(t, deviceB); len(state.Profiles) != 0 {
		t.Fatalf("profile must be tombstoned on B: %+v", state.Profiles)
	}
	assertDeviceTombstone(t, deviceB, hostID, KindKnownHost)
	assertDeviceTombstone(t, deviceB, profileID, KindAIProfile)

	// Idle sync must not revive deleted objects.
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("known host resurrected on B")
	}
	if state := deviceAIProfileState(t, deviceB); len(state.Profiles) != 0 {
		t.Fatal("profile resurrected on B")
	}
}

func TestEngineKnownHostLWWConflictConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:base", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// Both sides edit the same row offline; B's revision is higher.
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:a-edit", 200)
	putDeviceKnownHost(t, deviceB, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:b-edit", 300)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	for name, device := range map[string]*testDevice{"A": deviceA, "B": deviceB} {
		row, found := deviceKnownHost(t, device, hostID)
		if !found || row.Fingerprint != "SHA256:b-edit" || row.AddedAt != 300 {
			t.Fatalf("device%s known host=%+v found=%v, want B's edit", name, row, found)
		}
	}
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// Same revision with different content decides by canonical payload hash: both devices must converge to the same profile regardless of push order.
func TestEngineAIProfileEqualRevisionConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	profileID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: profileID, Name: "名字X", Model: "m", Stream: true}, 100)
	putDeviceAIProfile(t, deviceB, aiProfileRecord{ID: profileID, Name: "名字Y", Model: "m", Stream: true}, 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	nameA := deviceAIProfileState(t, deviceA).Profiles[0].Name
	nameB := deviceAIProfileState(t, deviceB).Profiles[0].Name
	if nameA != nameB {
		t.Fatalf("equal-revision LWW diverged: A=%q B=%q", nameA, nameB)
	}
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

func TestEngineAIProfileKeyedDeferredWhileVaultLocked(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newMasterTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:locked", 100)
	keyedID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, keyedID, "锁定档案", "locked-key", 100)
	keylessID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: keylessID, Name: "无密钥档案", Model: "m", Stream: true}, 100)

	deviceA.vault.Lock()
	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if report.Pushed != 2 {
		t.Fatalf("locked vault must defer only the keyed profile: %+v", report)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("deferred profile must surface a warning")
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("known host must sync while vault locked")
	}
	state := deviceAIProfileState(t, deviceB)
	if _, exists := state.find(keyedID); exists {
		t.Fatal("keyed profile must not sync while vault locked")
	}
	if _, exists := state.find(keylessID); !exists {
		t.Fatal("keyless profile must sync while vault locked")
	}

	if err := deviceA.vault.UnlockMaster(context.Background(), "device-master-pw"); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	keyed, exists := deviceAIProfileState(t, deviceB).find(keyedID)
	if !exists {
		t.Fatal("keyed profile must sync after unlock")
	}
	key, err := deviceB.vault.DecryptSecret(context.Background(), keyed.APIKey)
	if err != nil || key != "locked-key" {
		t.Fatalf("deviceB key=%q err=%v", key, err)
	}
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// M165 R2: opt-in is on by default; turning it off stops collection/apply and remote copies are kept.
func TestEngineKindOptInDefaultOnAndDisable(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:on", 100)
	profileID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: profileID, Name: "默认同步档案", Model: "m", Stream: true}, 100)

	if report := syncDevice(t, deviceA, server, "alice", "alice-pw-123"); report.Pushed != 2 {
		t.Fatalf("default opt-in must push both object types: %+v", report)
	}
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Applied != 2 {
		t.Fatalf("default opt-in must apply remote objects: %+v", report)
	}
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("default opt-in must apply known_host")
	}
	if state := deviceAIProfileState(t, deviceB); len(state.Profiles) != 1 {
		t.Fatalf("default opt-in must apply ai_profile: %+v", state.Profiles)
	}

	for _, device := range []*testDevice{deviceA, deviceB} {
		if err := device.db.SetKnownHostSyncOptIn(context.Background(), serverUserID(t, server, "alice", "alice-pw-123"), false); err != nil {
			t.Fatal(err)
		}
		if err := device.db.SetAIProfileSyncOptIn(context.Background(), serverUserID(t, server, "alice", "alice-pw-123"), false); err != nil {
			t.Fatal(err)
		}
	}
	putDeviceKnownHost(t, deviceA, ids.New(), "disabled.example.com", 22, "ssh-ed25519", "SHA256:disabled", 200)
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: ids.New(), Name: "关闭后不应推送", Model: "m", Stream: true}, 200)
	if report := syncDevice(t, deviceA, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("new objects must not be pushed after opt-in is disabled: %+v", report)
	}
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Applied != 0 {
		t.Fatalf("new objects must not be applied after opt-in is disabled: %+v", report)
	}
}

// M165 R2: on -> sync; off -> stop collection/apply/tombstone propagation (remote copies kept); on again -> catch-up pull and merge (nothing reconciled while off).
func TestEngineKindOptInOnOffOn(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	ctx := context.Background()

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:onoff", 100)

	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("known_host must sync after opt-in is enabled")
	}

	// A turns off: local additions are not pushed, and B's remote changes are not applied to A.
	if err := deviceA.db.SetKnownHostSyncOptIn(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	localOnly := ids.New()
	putDeviceKnownHost(t, deviceA, localOnly, "off.example.com", 22, "ssh-rsa", "SHA256:local-only", 200)
	fromB := ids.New()
	putDeviceKnownHost(t, deviceB, fromB, "from-b.example.com", 2222, "ssh-rsa", "SHA256:from-b", 200)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, localOnly); found {
		t.Fatal("local additions must not be pushed after opt-in is disabled")
	}
	if _, found := deviceKnownHost(t, deviceA, fromB); found {
		t.Fatal("remote objects must not be applied after opt-in is disabled")
	}

	// A deletes the local row (while off, opt-in off): no tombstone, no propagation, the remote copy is kept.
	if err := deviceA.db.KnownHostRemove(ctx, hostID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("deletions during opt-in off must not propagate; remote copy must be retained")
	}

	// Re-enabled: remote objects not reconciled while off (fromB) are pulled and merged; the locally deleted hostID (never tombstoned) does not revive (the remote copy stays on B and the server).
	enableDeviceKindOptIn(t, deviceA, userID)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceA, fromB); !found {
		t.Fatal("remote objects created while opt-in was off must be pulled after re-enabling")
	}
	if _, found := deviceKnownHost(t, deviceA, hostID); found {
		t.Fatal("local deletion during opt-in off must not revive (no deletion marker, deletion is local only)")
	}
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("remote copy must remain on B")
	}
}

// M165 R2: a device with opt-in off does not apply remote tombstones (local rows kept); after re-enabling, rows are cleared per the tombstones.
func TestEngineKindOptInRemoteTombstoneGated(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	ctx := context.Background()

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:gone", 100)
	putDeviceKnownHost(t, deviceB, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:gone", 100)
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// B turns off; A deletes and pushes the tombstone.
	if err := deviceB.db.SetKnownHostSyncOptIn(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	deleteDeviceKnownHost(t, deviceA, hostID, ids.NowMS())
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", hostID, KindTombstone)

	// B syncs: the tombstone is not applied, the local row is kept.
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("remote deletion markers must not delete local rows while opt-in is off")
	}

	// B re-enables: the tombstone (still unreconciled) is applied in the next round and the local row is cleared.
	enableDeviceKindOptIn(t, deviceB, userID)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("local rows must be cleared by deletion markers after re-enabling")
	}
}
