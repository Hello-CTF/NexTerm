package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestDesktopRepairUndecryptableRequiresFreshConfirmedSnapshot(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	oldDevice := newTestDevice(t)
	ctx := context.Background()
	restoreID, clearID := ids.New(), ids.New()
	putDeviceAsset(t, oldDevice, store.AssetRow{ID: restoreID, Kind: "ssh", Name: "旧密钥资产", CreatedAt: 100, UpdatedAt: 100})
	putDeviceAsset(t, oldDevice, store.AssetRow{ID: clearID, Kind: "ssh", Name: "旧密钥孤立资产", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, oldDevice, server, "alice", "alice-pw-123")
	resetDEK(t, server, "alice", "alice-pw-123")

	instance := newTestInstance(t, true)
	putTestAsset := func(name string, updatedAt int64) {
		t.Helper()
		if _, err := instance.db.AssetUpsert(ctx, store.AssetRow{ID: restoreID, Kind: "ssh", Name: name, CreatedAt: 100, UpdatedAt: updatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	putTestAsset("本机副本", 200)
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-pw-123"),
	}); err != nil {
		t.Fatal(err)
	}
	compare := func(id string) RepairUndecryptableRequest {
		t.Helper()
		result, err := instance.service.Compare(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range result.Rows {
			if row.ID == id {
				if row.State != CompareStateRemoteUndecryptable || row.RemoteSeq == 0 || row.RemoteBlobHash == "" || result.UserID == "" || result.CompareToken == "" {
					t.Fatalf("incomplete repair snapshot: result=%+v row=%+v", result, row)
				}
				return RepairUndecryptableRequest{
					ID: id, UserID: result.UserID, CompareToken: result.CompareToken,
					RemoteSeq: row.RemoteSeq, RemoteBlobHash: row.RemoteBlobHash,
					LocalKind: row.Kind, LocalPayloadHash: row.LocalPayloadHash,
				}
			}
		}
		t.Fatalf("missing compare row %s", id)
		return RepairUndecryptableRequest{}
	}
	cloudHash := func(id string) string {
		t.Helper()
		var blob []byte
		if err := server.db.DB().QueryRowContext(ctx, "SELECT blob FROM user_sync_object WHERE id = ?", id).Scan(&blob); err != nil {
			t.Fatal(err)
		}
		return hashBlobHex(blob)
	}

	restore := compare(restoreID)
	restore.Action = "restore-local"
	oldHash := cloudHash(restoreID)
	wrongAccount := restore
	wrongAccount.UserID = "another-user"
	if _, err := instance.service.RepairUndecryptable(ctx, wrongAccount); err == nil {
		t.Fatal("another account's comparison was accepted")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	putTestAsset("本机副本已修改", 250)
	if _, err := instance.service.RepairUndecryptable(ctx, restore); err == nil {
		t.Fatal("stale local snapshot was accepted")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	if cloudHash(restoreID) != oldHash {
		t.Fatal("stale local snapshot replaced cloud ciphertext")
	}

	restore = compare(restoreID)
	restore.Action = "restore-local"
	if _, err := instance.service.KindOptInSet(ctx, KindOptInPatch{KnownHost: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.RepairUndecryptable(ctx, restore); err == nil {
		t.Fatal("stale sync opt-in snapshot was accepted")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	if _, err := instance.service.KindOptInSet(ctx, KindOptInPatch{KnownHost: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	restore = compare(restoreID)
	restore.Action = "restore-local"
	result, err := instance.service.RepairUndecryptable(ctx, restore)
	if err != nil || !result.Repaired {
		t.Fatalf("restore failed: result=%+v err=%v", result, err)
	}
	if cloudHash(restoreID) == oldHash {
		t.Fatal("confirmed repair left the old ciphertext")
	}
	newDevice := newTestDevice(t)
	syncDevice(t, newDevice, server, "alice", "alice-pw-123")
	asset, err := newDevice.db.AssetGet(ctx, restoreID)
	if err != nil || asset.Name != "本机副本已修改" {
		t.Fatalf("repaired object not readable with current key: asset=%+v err=%v", asset, err)
	}

	clear := compare(clearID)
	clear.Action = "clear-cloud"
	if clear.LocalKind != "" || clear.LocalPayloadHash != "" {
		t.Fatalf("clear candidate unexpectedly has a local copy: %+v", clear)
	}
	// Another writer replaces the opaque cloud object after comparison; the old confirmation must not delete it.
	session, err := instance.service.engine.ensureSession(ctx, remoteConfigFromSecret(linkSecret{URL: server.URL, Username: "alice", Password: "alice-pw-123"}))
	if err != nil {
		t.Fatal(err)
	}
	idsResponse, err := session.client.ids(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replacement := []byte("another-unreadable-version")
	if _, err := session.client.push(ctx, idsResponse.Head, []WireObject{{ID: clearID, Blob: replacement}}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.RepairUndecryptable(ctx, clear); err == nil {
		t.Fatal("stale cloud snapshot was accepted")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	if cloudHash(clearID) != hashBlobHex(replacement) {
		t.Fatal("stale clear removed a newer cloud object")
	}

	clear = compare(clearID)
	clear.Action = "clear-cloud"
	result, err = instance.service.RepairUndecryptable(ctx, clear)
	if err != nil || !result.Repaired {
		t.Fatalf("clear failed: result=%+v err=%v", result, err)
	}
	idsResponse, err = session.client.ids(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range idsResponse.Entries {
		if entry.ID == clearID {
			t.Fatal("confirmed clear left the cloud object")
		}
	}
}

func TestDesktopRepairRejectsChangedAccountDEK(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	oldDevice := newTestDevice(t)
	ctx := context.Background()
	assetID := ids.New()
	putDeviceAsset(t, oldDevice, store.AssetRow{ID: assetID, Kind: "ssh", Name: "旧密钥资产", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, oldDevice, server, "alice", "alice-pw-123")
	resetDEK(t, server, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-pw-123"),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := instance.service.Compare(ctx)
	if err != nil || len(result.Rows) != 1 || result.Rows[0].State != CompareStateRemoteUndecryptable {
		t.Fatalf("compare=%+v err=%v", result, err)
	}
	row := result.Rows[0]
	request := RepairUndecryptableRequest{
		ID: assetID, Action: "clear-cloud", UserID: result.UserID, CompareToken: result.CompareToken,
		RemoteSeq: row.RemoteSeq, RemoteBlobHash: row.RemoteBlobHash,
	}
	// The account key changes again while the desktop session is cached. Repair must use the current account envelope.
	resetDEK(t, server, "alice", "alice-pw-123")
	if _, err := instance.service.RepairUndecryptable(ctx, request); err == nil {
		t.Fatal("cached stale DEK was accepted for destructive repair")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sync_object WHERE id = ?", assetID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("stale account key deleted the cloud object")
	}
}

func TestDesktopSyncAndCompareRejectCachedDEKAfterEnvelopeRotation(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	ctx := context.Background()
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-pw-123"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Compare(ctx); err != nil {
		t.Fatal(err)
	}

	// This direct envelope update intentionally leaves the old cookie valid.
	resetDEK(t, server, "alice", "alice-pw-123")
	assetID := ids.New()
	if _, err := instance.db.AssetUpsert(ctx, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "不得用旧密钥上传", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Sync(ctx); err == nil {
		t.Fatal("sync accepted a cached old DEK")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	if _, err := instance.service.Compare(ctx); err == nil {
		t.Fatal("compare presented data under a cached old DEK")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	status, err := instance.service.Status(ctx)
	if err != nil || status.LastError == "" {
		t.Fatalf("DEK mismatch was not visible in status: status=%+v err=%v", status, err)
	}
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sync_object WHERE id = ?", assetID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("cached old DEK uploaded local content")
	}
}

func TestDesktopClearUndecryptableKeepsLocalCopyWithSyncKindDisabled(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	oldDevice := newTestDevice(t)
	ctx := context.Background()
	objectID := ids.New()
	putDeviceAsset(t, oldDevice, store.AssetRow{ID: objectID, Kind: "ssh", Name: "旧密钥对象", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, oldDevice, server, "alice", "alice-pw-123")
	resetDEK(t, server, "alice", "alice-pw-123")

	instance := newTestInstance(t, true)
	if err := instance.db.GORM().WithContext(ctx).Create(&store.KnownHostRow{
		ID: objectID, Host: "host.example.test", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:local", AddedAt: 200,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-pw-123"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.KindOptInSet(ctx, KindOptInPatch{KnownHost: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	comparison, err := instance.service.Compare(ctx)
	if err != nil || len(comparison.Rows) != 1 {
		t.Fatalf("compare=%+v err=%v", comparison, err)
	}
	row := comparison.Rows[0]
	if row.ID != objectID || row.State != CompareStateRemoteUndecryptable || row.Kind != "" {
		t.Fatalf("disabled kind should be hidden from comparison: row=%+v", row)
	}
	request := RepairUndecryptableRequest{
		ID: objectID, Action: "clear-cloud", UserID: comparison.UserID, CompareToken: comparison.CompareToken,
		RemoteSeq: row.RemoteSeq, RemoteBlobHash: row.RemoteBlobHash,
	}
	if _, err := instance.service.RepairUndecryptable(ctx, request); err == nil {
		t.Fatal("clear removed a cloud object despite a hidden local copy")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	var count int
	if err := server.db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sync_object WHERE id = ?", objectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("hidden local copy did not prevent cloud deletion")
	}
}
