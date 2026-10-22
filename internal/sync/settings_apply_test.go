package sync

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func applyKnownHost(t *testing.T, id, host string, port int32, keyType, fingerprint string, addedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindKnownHost, Payload: applyPayload(t, knownHostObject{
		ID: id, Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, AddedAt: addedAt,
	})}
}

func applyKnownHostTombstone(t *testing.T, id string, deletedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindKnownHost, DeletedAt: deletedAt,
	})}
}

func applyKnownHostConflictTombstone(t *testing.T, id, host string, port int32, keyType string, deletedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindKnownHost, DeletedAt: deletedAt, Host: host, Port: port, KeyType: keyType,
	})}
}

func applyAIProfile(t *testing.T, payload aiProfileObject) ApplyObject {
	t.Helper()
	return ApplyObject{ID: payload.ID, Kind: KindAIProfile, Payload: applyPayload(t, payload)}
}

func applyAIProfileTombstone(t *testing.T, id string, deletedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindAIProfile, DeletedAt: deletedAt,
	})}
}

func fullTestAIProfile(id string) aiProfileObject {
	return aiProfileObject{
		ID: id, Name: "同步档案", BaseURL: "https://ai.example.com/v1", APIKey: "profile-secret-值",
		Model: "model-x", FallbackModel: "model-y", Temperature: testPtr(0.7), ReasoningEffort: "high", ContextWindow: 128000,
		MaxTokens: testPtr(4096), Proxy: testPtr("http://proxy.example.com:8080"), Stream: true,
		RequestTimeoutSeconds: testPtr(120), IdleTimeoutSeconds: testPtr(30),
		CircuitFailureThreshold: testPtr(5), CircuitCooldownSeconds: testPtr(60),
		UpdatedAt: 100,
	}
}

func requireKnownHost(t *testing.T, instance *testInstance, id, fingerprint string, addedAt int64) store.KnownHostRow {
	t.Helper()
	row, found, err := instance.service.engine.knownHostByID(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("known host %s not found: found=%v err=%v", id, found, err)
	}
	if fingerprint != "" && row.Fingerprint != fingerprint {
		t.Fatalf("known host %s fingerprint=%q, want %q", id, row.Fingerprint, fingerprint)
	}
	if addedAt != 0 && row.AddedAt != addedAt {
		t.Fatalf("known host %s revision=%d, want %d", id, row.AddedAt, addedAt)
	}
	return row
}

func requireNoKnownHost(t *testing.T, instance *testInstance, id string) {
	t.Helper()
	if _, found, err := instance.service.engine.knownHostByID(context.Background(), id); err != nil || found {
		t.Fatalf("known host %s must be absent: found=%v err=%v", id, found, err)
	}
}

func requireSyncTombstone(t *testing.T, instance *testInstance, id, kind string, deletedAt int64) {
	t.Helper()
	row, found, err := instance.service.engine.syncTombstoneGet(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("tombstone %s not found: found=%v err=%v", id, found, err)
	}
	if row.Kind != kind || row.DeletedAt != deletedAt {
		t.Fatalf("tombstone %s=%+v, want kind=%s deletedAt=%d", id, row, kind, deletedAt)
	}
}

func requireNoSyncTombstone(t *testing.T, instance *testInstance, id string) {
	t.Helper()
	if _, found, err := instance.service.engine.syncTombstoneGet(context.Background(), id); err != nil || found {
		t.Fatalf("tombstone %s must be cleared: found=%v err=%v", id, found, err)
	}
}

func requireAIProfileState(t *testing.T, instance *testInstance) (aiProfilesState, int64) {
	t.Helper()
	state, revision, _, err := instance.service.engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state, revision
}

func putTestAIProfile(t *testing.T, instance *testInstance, record aiProfileRecord, updatedAt int64) {
	t.Helper()
	engine := instance.service.engine
	state, _, _, err := engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.upsert(record)
	state.ensureActive()
	if err := engine.aiProfilesSave(context.Background(), state, updatedAt, ""); err != nil {
		t.Fatal(err)
	}
}

func sealedTestAIProfile(t *testing.T, instance *testInstance, id, secret string) aiProfileRecord {
	t.Helper()
	envelope, err := instance.vault.EncryptSecret(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return aiProfileRecord{ID: id, Name: "本地档案", BaseURL: "https://local.example.com", APIKey: envelope, Model: "local-model", Stream: true}
}

func TestApplyKnownHostRoundtripIdempotent(t *testing.T) {
	instance := newTestInstance(t, false)
	hostID := ids.New()
	object := applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:aaa", 100)

	requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultApplied)
	row := requireKnownHost(t, instance, hostID, "SHA256:aaa", 100)
	if row.Host != "git.example.com" || row.Port != 22 || row.KeyType != "ssh-ed25519" {
		t.Fatalf("known host fields mangled: %+v", row)
	}
	requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultIdentical)
}

func TestApplyKnownHostNoRevisionRollback(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	hostID := ids.New()
	if _, err := instance.db.DB().ExecContext(ctx,
		`INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)`,
		hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:new", 200); err != nil {
		t.Fatal(err)
	}

	result := mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:old", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, hostID, "SHA256:new", 200)
}

func TestApplyKnownHostTombstoneLifecycle(t *testing.T) {
	instance := newTestInstance(t, false)
	hostID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:aaa", 100))

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, hostID, 200)).Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, hostID)
	requireSyncTombstone(t, instance, hostID, KindKnownHost, 200)

	stale := mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:old", 150))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	requireNoKnownHost(t, instance, hostID)

	newer := mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:new", 300))
	requireApplyResult(t, newer.Objects[0], ApplyResultApplied)
	requireKnownHost(t, instance, hostID, "SHA256:new", 300)
	requireNoSyncTombstone(t, instance, hostID)
}

func TestApplyKnownHostTripleConflictDisplacesLoser(t *testing.T) {
	instance := newTestInstance(t, false)
	localID := ids.New()
	remoteID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, localID, "git.example.com", 22, "ssh-ed25519", "SHA256:local", 100))

	result := mustApplyObjects(t, instance, applyKnownHost(t, remoteID, "git.example.com", 22, "ssh-ed25519", "SHA256:remote", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, localID)
	requireKnownHost(t, instance, remoteID, "SHA256:remote", 200)
	requireSyncTombstone(t, instance, localID, KindKnownHost, 200)

	stale := mustApplyObjects(t, instance, applyKnownHost(t, localID, "git.example.com", 22, "ssh-ed25519", "SHA256:local", 100))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	requireNoKnownHost(t, instance, localID)
}

func TestApplyKnownHostTripleConflictLocalNewerKeepsRow(t *testing.T) {
	instance := newTestInstance(t, false)
	localID := ids.New()
	remoteID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, localID, "git.example.com", 22, "ssh-ed25519", "SHA256:local", 300))

	result := mustApplyObjects(t, instance, applyKnownHost(t, remoteID, "git.example.com", 22, "ssh-ed25519", "SHA256:remote", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, localID, "SHA256:local", 300)
	requireNoKnownHost(t, instance, remoteID)
	requireNoSyncTombstone(t, instance, localID)
	// The loser (remote) must be tombstoned at the winner's revision, overwriting the server-side loser object with this round's push.
	requireSyncTombstone(t, instance, remoteID, KindKnownHost, 300)
}

// The re-IDed new ID must beat the old payload in the same-revision hash tiebreak: in both hash orderings the loser ordering is constructively rejected,
// so the old copy can only take the displaced path and never tombstones the re-IDed object in reverse.
func TestKnownHostReincarnationIDWinsHashTiebreak(t *testing.T) {
	instance := newTestInstance(t, false)
	engine := instance.service.engine
	row := &store.KnownHostRow{ID: ids.New(), Host: "old.example.com", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:trusted", AddedAt: 50}
	oldPayload, err := marshalObject(knownHostObject{
		ID: row.ID, Host: row.Host, Port: row.Port, KeyType: row.KeyType, Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 64; iteration++ {
		newID, err := engine.knownHostReincarnationID(row)
		if err != nil {
			t.Fatal(err)
		}
		newPayload, err := marshalObject(knownHostObject{
			ID: newID, Host: row.Host, Port: row.Port, KeyType: row.KeyType, Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !remoteWins(row.AddedAt, row.AddedAt, newPayload, oldPayload) {
			t.Fatalf("iteration %d: reincarnation %s loses the equal-revision hash tiebreak", iteration, newID)
		}
	}
}

// A local row with the same ID as the payload but a different triple is an "incarnation": it cannot coexist with a same-ID loser tombstone (slot ambiguity,
// the server would not converge, and later same-ID tombstones would mistakenly delete it). The incarnation must yield its original ID in the same transaction - the trusted content is
// preserved verbatim under the new ID, and the original ID is tombstoned for the remote loser; rows with different triples are unaffected.
func TestApplyKnownHostTripleConflictIncarnationReIDed(t *testing.T) {
	instance := newTestInstance(t, false)
	sharedID := ids.New()
	winnerID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:unrelated", 50))
	mustApplyObjects(t, instance, applyKnownHost(t, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200))

	// The payload carries sharedID with a newer revision (50 -> 100) migrating the triple, but the target triple's winner (200) is stronger:
	// the migration is rejected, sharedID is tombstoned, and the incarnation is preserved under a new ID (content and revision unchanged).
	result := mustApplyObjects(t, instance, applyKnownHost(t, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireSyncTombstone(t, instance, sharedID, KindKnownHost, 200)
	requireNoKnownHost(t, instance, sharedID)
	requireKnownHost(t, instance, winnerID, "SHA256:winner", 200)

	rows, err := instance.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	var incarnation *store.KnownHostRow
	for index := range rows {
		if rows[index].Host == "old.example.com" {
			incarnation = &rows[index]
		}
	}
	if incarnation == nil || incarnation.ID == sharedID || incarnation.Fingerprint != "SHA256:unrelated" ||
		incarnation.AddedAt != 50 || incarnation.KeyType != "ssh-rsa" || incarnation.Port != 22 {
		t.Fatalf("incarnation must survive under a fresh id with content intact: %+v", rows)
	}

	// The collect map must no longer have a same-ID slot conflict: the incarnation object and the original-ID tombstone are separate objects.
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	if entry, exists := objects[sharedID]; !exists || entry.kind != KindTombstone {
		t.Fatalf("original id slot must carry the tombstone: %+v", objects[sharedID])
	}
	if entry, exists := objects[incarnation.ID]; !exists || entry.kind != KindKnownHost {
		t.Fatalf("incarnation must be collected as a known_host object: %+v", entry)
	}

	// A later tombstone with the original ID (higher revision) does not mistakenly delete the re-IDed incarnation.
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, sharedID, 300)).Objects[0], ApplyResultApplied)
	rows, err = instance.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("tombstone replay must not delete the re-IDed record: %+v err=%v", rows, err)
	}
	stillThere := false
	for _, row := range rows {
		if row.ID == incarnation.ID && row.Fingerprint == "SHA256:unrelated" {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatalf("incarnation lost after same-id tombstone replay: %+v", rows)
	}
}

// A conflict tombstone (carrying the original loser triple) meeting a newer winning incarnation with the same ID but a different triple: it must not be cleared
// as a user deletion; the incarnation must be safely re-IDed with content and revision preserved, and the conflict tombstone only clears the original slot
// and re-propagates with collection (triple preserved).
func TestApplyKnownHostConflictTombstoneReIDsNewerIncarnation(t *testing.T) {
	instance := newTestInstance(t, false)
	sharedID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:newer", 60))

	result := mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, sharedID, "git.example.com", 22, "ssh-ed25519", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, sharedID)
	requireSyncTombstone(t, instance, sharedID, KindKnownHost, 200)

	rows, err := instance.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	incarnation := rows[0]
	if incarnation.ID == sharedID || incarnation.Host != "old.example.com" || incarnation.Fingerprint != "SHA256:newer" || incarnation.AddedAt != 60 {
		t.Fatalf("newer incarnation must survive under a fresh id: %+v", incarnation)
	}

	// Collect: the original ID slot is the conflict tombstone carrying the triple, the new ID is the live object.
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := objects[sharedID]
	if entry.kind != KindTombstone || !strings.Contains(string(entry.plaintext), `"host":"git.example.com"`) {
		t.Fatalf("conflict tombstone must carry the loser triple: %+v", entry)
	}
	if entry := objects[incarnation.ID]; entry.kind != KindKnownHost {
		t.Fatalf("incarnation must be collected as a known_host object: %+v", entry)
	}

	// Same-triple conflict tombstone replay (loser cleanup from another device): the original-ID row is gone locally, so only the tombstone merges and the incarnation is unaffected.
	replay := mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, sharedID, "git.example.com", 22, "ssh-ed25519", 200))
	requireApplyResult(t, replay.Objects[0], ApplyResultApplied)
	if row, found, _ := instance.service.engine.knownHostByID(context.Background(), incarnation.ID); !found || row.Fingerprint != "SHA256:newer" {
		t.Fatalf("replay must not touch the re-IDed incarnation: %+v found=%v", row, found)
	}
}

// User-initiated deletion tombstones (no triple) take effect by ID regardless of the local row's triple; re-propagation must not carry a triple.
func TestApplyKnownHostUserTombstoneDeletesRegardlessOfTriple(t *testing.T) {
	instance := newTestInstance(t, false)
	rowID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, rowID, "old.example.com", 22, "ssh-rsa", "SHA256:fp", 60))

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, rowID, 100)).Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, rowID)
	requireSyncTombstone(t, instance, rowID, KindKnownHost, 100)

	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := objects[rowID]
	if entry.kind != KindTombstone || strings.Contains(string(entry.plaintext), "old.example.com") {
		t.Fatalf("user tombstone must stay plain: %+v", entry)
	}
}

// A newer local row revision: the conflict tombstone must neither clear nor re-ID it; the local row re-pushes the slot per standard anti-revival semantics.
func TestApplyKnownHostConflictTombstoneNewerRowSkips(t *testing.T) {
	instance := newTestInstance(t, false)
	rowID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, rowID, "old.example.com", 22, "ssh-rsa", "SHA256:newest", 300))

	result := mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, rowID, "git.example.com", 22, "ssh-ed25519", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, rowID, "SHA256:newest", 300)
	requireNoSyncTombstone(t, instance, rowID)
}

// Multiple conflict tombstones for the same ID: the triple metadata deterministically follows the highest revision, never mixing tombstones;
// a later higher-revision user deletion tombstone then clears the metadata, and re-propagation degrades to a plain user deletion.
func TestApplyKnownHostConflictTombstoneMetaFollowsMaxRevision(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	hostID := ids.New()

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, hostID, "a.example.com", 22, "ssh-rsa", 50)).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, hostID, "b.example.com", 2222, "ssh-ed25519", 200)).Objects[0], ApplyResultApplied)
	assertTombstoneTriple := func(wantHost string, wantDeletedAt int64) {
		t.Helper()
		objects, err := instance.service.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
		if err != nil {
			t.Fatal(err)
		}
		entry := objects[hostID]
		if entry.kind != KindTombstone {
			t.Fatalf("slot must carry a tombstone: %+v", entry)
		}
		if wantHost == "" {
			if strings.Contains(string(entry.plaintext), "example.com") {
				t.Fatalf("user tombstone must not carry a triple: %s", entry.plaintext)
			}
			return
		}
		if !strings.Contains(string(entry.plaintext), `"host":"`+wantHost+`"`) || !strings.Contains(string(entry.plaintext), `"deletedAt":`+strconv.FormatInt(wantDeletedAt, 10)) {
			t.Fatalf("tombstone must carry triple %s at revision %d: %s", wantHost, wantDeletedAt, entry.plaintext)
		}
	}
	assertTombstoneTriple("b.example.com", 200)

	// Reverse landing order: a later lower-revision landing must not overwrite the higher-revision metadata.
	otherID := ids.New()
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, otherID, "b.example.com", 2222, "ssh-ed25519", 200)).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, otherID, "a.example.com", 22, "ssh-rsa", 50)).Objects[0], ApplyResultApplied)
	objects, err := instance.service.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	if entry := objects[otherID]; !strings.Contains(string(entry.plaintext), `"host":"b.example.com"`) {
		t.Fatalf("lower revision must not overwrite the meta: %s", entry.plaintext)
	}

	// A higher-revision user deletion: the metadata is cleared and re-propagation is a plain user deletion.
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, hostID, 300)).Objects[0], ApplyResultApplied)
	assertTombstoneTriple("", 0)
}

func TestApplyKnownHostTripleConflictRemoteLoserTombstoned(t *testing.T) {
	instance := newTestInstance(t, false)
	winnerID := ids.New()
	loserID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200))

	// The loser arrives after the winner pushed: the local winner keeps its row, the loser id is tombstoned at the winner's revision, and this round's collect/push overwrites the server-side loser object.
	result := mustApplyObjects(t, instance, applyKnownHost(t, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, winnerID, "SHA256:winner", 200)
	requireNoKnownHost(t, instance, loserID)
	requireSyncTombstone(t, instance, loserID, KindKnownHost, 200)

	// The tombstone blocks loser replay; a newer loser can still win and tombstone the winner in turn.
	stale := mustApplyObjects(t, instance, applyKnownHost(t, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser", 100))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	requireNoKnownHost(t, instance, loserID)

	newer := mustApplyObjects(t, instance, applyKnownHost(t, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser-new", 300))
	requireApplyResult(t, newer.Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, winnerID)
	requireKnownHost(t, instance, loserID, "SHA256:loser-new", 300)
	requireSyncTombstone(t, instance, winnerID, KindKnownHost, 300)
}

// Same-triple rows with different IDs at the same revision must decide by canonical payload hash: both application orders converge to the same final state.
func TestApplyKnownHostEqualRevisionConverges(t *testing.T) {
	hostA := ids.New()
	hostB := ids.New()
	objectA := applyKnownHost(t, hostA, "git.example.com", 22, "ssh-ed25519", "SHA256:aaa", 100)
	objectB := applyKnownHost(t, hostB, "git.example.com", 22, "ssh-ed25519", "SHA256:bbb", 100)

	finalRow := func(objects ...ApplyObject) store.KnownHostRow {
		t.Helper()
		instance := newTestInstance(t, false)
		mustApplyObjects(t, instance, objects...)
		rows, err := instance.db.KnownHostList(context.Background())
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		return rows[0]
	}

	rowAB := finalRow(objectA, objectB)
	rowBA := finalRow(objectB, objectA)
	if rowAB.ID != rowBA.ID || rowAB.Fingerprint != rowBA.Fingerprint {
		t.Fatalf("equal-revision LWW depends on order: AB=%+v BA=%+v", rowAB, rowBA)
	}
}

func TestApplyAIProfileRoundtripPreservesFieldsAndSealsKey(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	payload := fullTestAIProfile(profileID)

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultApplied)

	state, revision := requireAIProfileState(t, instance)
	if revision != payload.UpdatedAt {
		t.Fatalf("setting revision=%d, want %d", revision, payload.UpdatedAt)
	}
	record, exists := state.find(profileID)
	if !exists {
		t.Fatal("profile missing after apply")
	}
	if record.Name != payload.Name || record.BaseURL != payload.BaseURL || record.Model != payload.Model ||
		record.FallbackModel != payload.FallbackModel || !float64PointersEqual(record.Temperature, payload.Temperature) ||
		record.ReasoningEffort != payload.ReasoningEffort ||
		record.ContextWindow != payload.ContextWindow || record.MaxTokens == nil || *record.MaxTokens != *payload.MaxTokens ||
		record.Proxy == nil || *record.Proxy != *payload.Proxy || record.Stream != payload.Stream ||
		record.RequestTimeoutSeconds == nil || *record.RequestTimeoutSeconds != *payload.RequestTimeoutSeconds ||
		record.IdleTimeoutSeconds == nil || *record.IdleTimeoutSeconds != *payload.IdleTimeoutSeconds ||
		record.CircuitFailureThreshold == nil || *record.CircuitFailureThreshold != *payload.CircuitFailureThreshold ||
		record.CircuitCooldownSeconds == nil || *record.CircuitCooldownSeconds != *payload.CircuitCooldownSeconds {
		t.Fatalf("profile fields not preserved: %+v", record)
	}
	if !strings.HasPrefix(record.APIKey, store.SecretEnvelopePrefix) {
		t.Fatalf("apiKey must be stored as envelope, got %q", record.APIKey)
	}
	stored, found, err := instance.db.SettingGet(context.Background(), store.AIProfilesSettingKey)
	if err != nil || !found {
		t.Fatalf("setting read: found=%v err=%v", found, err)
	}
	if strings.Contains(stored, payload.APIKey) {
		t.Fatal("plaintext apiKey persisted in setting")
	}
	if state.ActiveID == nil || *state.ActiveID != profileID {
		t.Fatalf("first profile must become active: %+v", state.ActiveID)
	}

	plaintext, err := instance.vault.DecryptSecret(context.Background(), record.APIKey)
	if err != nil || plaintext != payload.APIKey {
		t.Fatalf("stored envelope must open to the payload key: %q err=%v", plaintext, err)
	}

	// The canonical payload rebuilt from the persisted form must be byte-identical to the applied payload (including the revealed apiKey).
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	local, exists := objects[profileID]
	if !exists || local.kind != KindAIProfile {
		t.Fatalf("collected objects missing profile: %+v", local)
	}
	if string(local.plaintext) != string(applyPayload(t, payload)) {
		t.Fatalf("collect roundtrip mismatch:\n%s\n%s", local.plaintext, applyPayload(t, payload))
	}

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultIdentical)
}

func TestApplyAIProfileNoRevisionRollback(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	putTestAIProfile(t, instance, aiProfileRecord{ID: profileID, Name: "本地新名", Model: "m", Stream: true}, 200)

	stale := fullTestAIProfile(profileID)
	stale.Name = "远端旧名"
	stale.APIKey = ""
	stale.UpdatedAt = 100
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, stale)).Objects[0], ApplyResultSkipped)

	state, revision := requireAIProfileState(t, instance)
	if revision != 200 {
		t.Fatalf("revision rolled back to %d", revision)
	}
	record, _ := state.find(profileID)
	if record.Name != "本地新名" {
		t.Fatalf("older payload rolled back local profile: %+v", record)
	}
}

func TestApplyAIProfileTombstoneLifecycle(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	payload := fullTestAIProfile(profileID)
	mustApplyObjects(t, instance, applyAIProfile(t, payload))

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfileTombstone(t, profileID, 200)).Objects[0], ApplyResultApplied)
	state, _ := requireAIProfileState(t, instance)
	if _, exists := state.find(profileID); exists {
		t.Fatal("profile must be removed by tombstone")
	}
	requireSyncTombstone(t, instance, profileID, KindAIProfile, 200)

	stale := fullTestAIProfile(profileID)
	stale.UpdatedAt = 150
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, stale)).Objects[0], ApplyResultSkipped)
	if state, _ := requireAIProfileState(t, instance); len(state.Profiles) != 0 {
		t.Fatalf("stale profile resurrected under tombstone: %+v", state.Profiles)
	}

	newer := fullTestAIProfile(profileID)
	newer.Name = "新版本档案"
	newer.UpdatedAt = 300
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, newer)).Objects[0], ApplyResultApplied)
	state, _ = requireAIProfileState(t, instance)
	record, exists := state.find(profileID)
	if !exists || record.Name != "新版本档案" {
		t.Fatalf("newer profile must win: %+v", record)
	}
	requireNoSyncTombstone(t, instance, profileID)
}

func TestApplyAIProfileKeylessAppliesWhileVaultLocked(t *testing.T) {
	instance := newTestInstance(t, true)
	instance.vault.Lock()
	keyless := fullTestAIProfile(ids.New())
	keyless.APIKey = ""

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, keyless)).Objects[0], ApplyResultApplied)

	keyed := fullTestAIProfile(ids.New())
	result := mustApplyObjects(t, instance, applyAIProfile(t, keyed))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, "凭据库") {
		t.Fatalf("locked vault warning=%q", result.Objects[0].Warning)
	}
	state, _ := requireAIProfileState(t, instance)
	if _, exists := state.find(keyed.ID); exists {
		t.Fatal("keyed profile must not apply while vault locked")
	}
	if _, exists := state.find(keyless.ID); !exists {
		t.Fatal("keyless profile must apply while vault locked")
	}
}

func TestApplyAIProfileKeyedSkippedWhenVaultUnavailable(t *testing.T) {
	instance := newTestInstance(t, false)
	profileID := ids.New()
	result := mustApplyObjects(t, instance, applyAIProfile(t, fullTestAIProfile(profileID)))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, "凭据库") {
		t.Fatalf("unavailable vault warning=%q", result.Objects[0].Warning)
	}
}

// Profiles share the LWW clock: an older object must not lower the setting revision, and newer local edits are not rolled back.
func TestApplyAIProfileSharedClockNeverLowersRevision(t *testing.T) {
	instance := newTestInstance(t, true)
	keeperID := ids.New()
	putTestAIProfile(t, instance, aiProfileRecord{ID: keeperID, Name: "keeper", Model: "m", Stream: true}, 500)

	older := fullTestAIProfile(ids.New())
	older.UpdatedAt = 100
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, older)).Objects[0], ApplyResultApplied)

	_, revision := requireAIProfileState(t, instance)
	if revision != 500 {
		t.Fatalf("blob revision lowered to %d", revision)
	}
}

// The synced ai.models must be directly consumable by profiles.Manager: fields do not drift, and keys resolve via the local vault.
func TestApplyAIProfileReadableByProfilesManager(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	payload := fullTestAIProfile(profileID)
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultApplied)

	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, instance.db)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Profiles) != 1 || overview.Profiles[0].ID != profileID {
		t.Fatalf("overview=%+v", overview)
	}
	if overview.Profiles[0].APIKey != profiles.MaskedAPIKey {
		t.Fatalf("overview must mask the key: %q", overview.Profiles[0].APIKey)
	}
	profile, exists, err := manager.Profile(ctx, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("profile missing from manager")
	}
	if profile.Name != payload.Name || profile.BaseURL != payload.BaseURL || profile.Model != payload.Model ||
		profile.FallbackModel != payload.FallbackModel || !float64PointersEqual(profile.Temperature, payload.Temperature) ||
		string(profile.ReasoningEffort) != payload.ReasoningEffort ||
		profile.ContextWindow != payload.ContextWindow || profile.MaxTokens == nil || *profile.MaxTokens != *payload.MaxTokens ||
		profile.Proxy == nil || *profile.Proxy != *payload.Proxy || profile.Stream != payload.Stream ||
		profile.RequestTimeoutSeconds == nil || *profile.RequestTimeoutSeconds != *payload.RequestTimeoutSeconds ||
		profile.IdleTimeoutSeconds == nil || *profile.IdleTimeoutSeconds != *payload.IdleTimeoutSeconds ||
		profile.CircuitFailureThreshold == nil || *profile.CircuitFailureThreshold != *payload.CircuitFailureThreshold ||
		profile.CircuitCooldownSeconds == nil || *profile.CircuitCooldownSeconds != *payload.CircuitCooldownSeconds {
		t.Fatalf("manager sees drifted fields: %+v", profile)
	}
	config, ok, err := manager.ActiveConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || config.APIKey != payload.APIKey {
		t.Fatalf("manager must resolve the applied key: %+v ok=%v", config, ok)
	}
}

func TestApplySettingsObjectsMalformedRejected(t *testing.T) {
	instance := newTestInstance(t, false)
	unknownField := ids.New()
	mismatchID := ids.New()

	objects := []ApplyObject{
		{ID: unknownField, Kind: KindKnownHost, Payload: json.RawMessage(`{"id":"` + unknownField + `","host":"h","port":22,"keyType":"ssh-ed25519","fingerprint":"f","addedAt":1,"bogus":true}`)},
		{ID: mismatchID, Kind: KindKnownHost, Payload: applyPayload(t, knownHostObject{ID: ids.New(), Host: "h", Port: 22, KeyType: "k", Fingerprint: "f", AddedAt: 1})},
		{ID: ids.New(), Kind: KindAIProfile, Payload: json.RawMessage(`{"id":"x","bogus":1}`)},
		{ID: ids.New(), Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindKnownHost, DeletedAt: 1})[:0]},
	}
	result := mustApplyObjects(t, instance, objects...)
	if result.Skipped != 4 {
		t.Fatalf("result=%+v", result)
	}
	for index, entry := range result.Objects {
		requireApplyResult(t, entry, ApplyResultSkipped)
		if entry.Warning == "" {
			t.Fatalf("malformed object %d must carry a warning", index)
		}
	}
	if rows, err := instance.db.KnownHostList(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("malformed known host applied: %+v err=%v", rows, err)
	}
}
