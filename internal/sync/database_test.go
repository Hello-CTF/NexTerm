package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func newDatabaseSyncStore(t *testing.T) *store.Store {
	t.Helper()
	return dbtest.NewFixture(t).OpenStore(t)
}

func createDatabaseSyncUser(t *testing.T, ctx context.Context, db *store.Store) string {
	t.Helper()
	userID := "pg-user-" + ids.New()
	if err := db.GORM().WithContext(ctx).Create(&store.AppUser{ID: userID, Username: userID, Role: "user", PasswordHash: "x", State: "active", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	return userID
}

func newDatabaseTestInstance(t *testing.T, unlocked bool) *testInstance {
	t.Helper()
	ctx := context.Background()
	db := newDatabaseSyncStore(t)
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, db)
	if unlocked {
		if err := credentialVault.InitMaster(ctx, "sync-test-master"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(credentialVault.Lock)
	}
	return &testInstance{db: db, vault: credentialVault, service: New(db, credentialVault, WithMetadata("test", true))}
}

func TestDatabaseObjectStorePushPullAndHeadConflict(t *testing.T) {
	db := newDatabaseSyncStore(t)
	ctx := context.Background()
	userID := createDatabaseSyncUser(t, ctx, db)
	objectStore := &objectStore{db: db.GORM()}

	if head, err := objectStore.currentHead(ctx, userID); err != nil || head != genesisHead(userID) {
		t.Fatalf("genesis head=%q err=%v", head, err)
	}
	objects := []WireObject{{ID: "obj-a", Blob: []byte("blob-a")}, {ID: "obj-b", Blob: []byte("blob-b")}}
	applied, skipped, head, maxSeq, err := objectStore.push(ctx, userID, genesisHead(userID), objects)
	if err != nil || applied != 2 || skipped != 0 || maxSeq != 2 {
		t.Fatalf("push applied=%d skipped=%d maxSeq=%d err=%v", applied, skipped, maxSeq, err)
	}
	if _, _, _, _, err := objectStore.push(ctx, userID, genesisHead(userID), []WireObject{{ID: "obj-c", Blob: []byte("v2")}}); err != errHeadMismatch {
		t.Fatalf("stale head push err=%v, want errHeadMismatch", err)
	}
	applied, skipped, head2, maxSeq2, err := objectStore.push(ctx, userID, head, objects)
	if err != nil || applied != 0 || skipped != 2 || head2 != head || maxSeq2 != maxSeq {
		t.Fatalf("idempotent push applied=%d skipped=%d headSame=%v maxSeq=%d err=%v", applied, skipped, head2 == head, maxSeq2, err)
	}
	pulled, _, pulledMaxSeq, nextSeq, done, err := objectStore.pull(ctx, userID, 0, nil, 0)
	if err != nil || len(pulled) != 2 || pulled[0].ID != "obj-a" || pulled[1].ID != "obj-b" || !done || pulledMaxSeq != 2 || nextSeq != 2 {
		t.Fatalf("pulled=%+v done=%v maxSeq=%d nextSeq=%d err=%v", pulled, done, pulledMaxSeq, nextSeq, err)
	}
	entries, _, _, err := objectStore.idList(ctx, userID)
	if err != nil || len(entries) != 2 || entries[0].BlobHash != hashBlobHex([]byte("blob-a")) || entries[1].BlobHash != hashBlobHex([]byte("blob-b")) {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestDatabaseObjectStoreConcurrentPush(t *testing.T) {
	db := newDatabaseSyncStore(t)
	ctx := context.Background()
	userID := createDatabaseSyncUser(t, ctx, db)
	objectStore := &objectStore{db: db.GORM()}

	head := pushAllConcurrently(t, ctx, objectStore, userID, 8, 4)
	if head == genesisHead(userID) {
		t.Fatal("head did not advance")
	}
	assertSeqContiguous(t, ctx, objectStore, userID, 32)
	pulled, _, maxSeq, nextSeq, done, err := objectStore.pull(ctx, userID, 0, nil, 0)
	if err != nil || len(pulled) != 32 || !done || maxSeq != 32 || nextSeq != 32 {
		t.Fatalf("pulled=%d done=%v maxSeq=%d nextSeq=%d err=%v", len(pulled), done, maxSeq, nextSeq, err)
	}
	for i, object := range pulled {
		if object.Seq != int64(i+1) {
			t.Fatalf("pulled[%d] seq=%d id=%s", i, object.Seq, object.ID)
		}
	}
}

func TestDatabaseSyncTombstoneKeepsMaxDeletedAt(t *testing.T) {
	db := newDatabaseSyncStore(t)
	ctx := context.Background()
	engine := NewEngine(db, nil, nil)
	id := ids.New()

	if err := engine.syncTombstonePut(ctx, id, KindGroup, 100); err != nil {
		t.Fatal(err)
	}
	if err := engine.syncTombstonePut(ctx, id, KindGroup, 50); err != nil {
		t.Fatal(err)
	}
	row, found, err := engine.syncTombstoneGet(ctx, id)
	if err != nil || !found || row.Kind != KindGroup || row.DeletedAt != 100 {
		t.Fatalf("GREATEST regression: older overwrote newer: %+v found=%v err=%v", row, found, err)
	}
	if err := engine.syncTombstonePut(ctx, id, KindGroup, 200); err != nil {
		t.Fatal(err)
	}
	row, found, err = engine.syncTombstoneGet(ctx, id)
	if err != nil || !found || row.DeletedAt != 200 {
		t.Fatalf("newer tombstone must win: %+v found=%v err=%v", row, found, err)
	}
}

func TestDatabaseEngineCanonicalApply(t *testing.T) {
	instance := newDatabaseTestInstance(t, true)
	ctx := context.Background()

	groupID := ids.New()
	requireApplyResult(t, mustApplyObjects(t, instance, applyGroupObject(t, groupID, "PG 分组", 100)).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, applyGroupObject(t, groupID, "PG 分组", 100)).Objects[0], ApplyResultIdentical)

	snippetID := ids.New()
	snippet := ApplyObject{ID: snippetID, Kind: KindSnippet, Payload: applyPayload(t, snippetObject{
		ID: snippetID, Name: "PG 片段", Body: "body", CreatedAt: 1, UpdatedAt: 100,
	})}
	requireApplyResult(t, mustApplyObjects(t, instance, snippet).Objects[0], ApplyResultApplied)
	stored, err := instance.db.SnippetGet(ctx, snippetID)
	if err != nil || stored.Name != "PG 片段" || stored.Body != "body" || stored.UpdatedAt != 100 {
		t.Fatalf("snippet=%+v err=%v", stored, err)
	}

	credentialID := ids.New()
	credential := ApplyObject{ID: credentialID, Kind: KindCredential, Payload: applyPayload(t, credentialObject{
		ID: credentialID, Name: "PG 凭据", Kind: "password", Secret: "s3cret", UpdatedAt: 100,
	})}
	requireApplyResult(t, mustApplyObjects(t, instance, credential).Objects[0], ApplyResultApplied)
	row, err := instance.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "s3cret" {
		t.Fatalf("credential secret=%q err=%v", secret, err)
	}

	tombstone := ApplyObject{ID: groupID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindGroup, DeletedAt: 200,
	})}
	requireApplyResult(t, mustApplyObjects(t, instance, tombstone).Objects[0], ApplyResultApplied)
	if _, err := instance.db.GroupGet(ctx, groupID); !isNotFound(err) {
		t.Fatalf("group must be deleted by tombstone: %v", err)
	}
	assertDeviceTombstone(t, &testDevice{db: instance.db}, groupID, KindGroup)
}

func TestDatabaseEngineSyncE2E(t *testing.T) {
	db := newDatabaseSyncStore(t)
	server := newTestSyncServerOn(t, db)
	ctx := context.Background()

	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("alice-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	user, err := server.accounts.CreateUserWithEnvelopes(ctx, "alice", "alice", "alice-pw-123", envelopes)
	if err != nil {
		t.Fatal(err)
	}

	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	groupID := ids.New()
	putDeviceGroup(t, deviceA, groupID, nil, "PG 分组", 100)
	snippetID := ids.New()
	putDeviceSnippet(t, deviceA, snippetID, "PG 片段", "body", 100)
	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "PG 凭据", "s3cret", 100)

	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pushed != 3 {
		t.Fatalf("deviceA pushed=%d want 3: %+v", reportA.Pushed, reportA)
	}
	var serverObjects int
	if err := db.GORM().WithContext(ctx).Raw("SELECT count(*) FROM user_sync_object WHERE user_id = ?", user.ID).Row().Scan(&serverObjects); err != nil || serverObjects != 3 {
		t.Fatalf("server objects=%d err=%v", serverObjects, err)
	}

	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	group, err := deviceB.db.GroupGet(ctx, groupID)
	if err != nil || group.Name != "PG 分组" {
		t.Fatalf("deviceB group=%+v err=%v", group, err)
	}
	snippet, err := deviceB.db.SnippetGet(ctx, snippetID)
	if err != nil || snippet.Body != "body" {
		t.Fatalf("deviceB snippet=%+v err=%v", snippet, err)
	}
	credential, err := deviceB.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := deviceB.vault.DecryptCredentialString(ctx, credential)
	if err != nil || secret != "s3cret" {
		t.Fatalf("deviceB secret=%q err=%v", secret, err)
	}

	if err := deviceA.db.SnippetDelete(ctx, snippetID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.SnippetGet(ctx, snippetID); !isNotFound(err) {
		t.Fatalf("snippet must be tombstoned on B: %v", err)
	}
	if report := syncDevice(t, deviceA, server, "alice", "alice-pw-123"); report.Pulled+report.Applied+report.Pushed != 0 {
		t.Fatalf("deviceA must be quiescent: %+v", report)
	}
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Pulled+report.Applied+report.Pushed != 0 {
		t.Fatalf("deviceB must be quiescent: %+v", report)
	}
}
