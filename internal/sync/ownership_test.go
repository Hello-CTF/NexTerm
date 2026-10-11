package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestImportAndKnownHostOwnershipIsolation(t *testing.T) {
	instance := newTestInstance(t, true)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	groupID, assetID, credentialID, snippetID := ids.New(), ids.New(), ids.New(), ids.New()
	bundle := SyncBundle{
		Protocol: BundleProtocolVersion,
		Groups:   []SyncBundleGroup{{ID: groupID, Name: "group-a", CreatedAt: 1, UpdatedAt: 1}},
		Assets: []SyncBundleAsset{{
			ID: assetID, GroupID: &groupID, Kind: "ssh", Name: "asset-a", CredID: &credentialID,
			OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 2,
		}},
		Creds:    []SyncBundleCredential{{ID: credentialID, Name: "credential-a", Kind: "password", Secret: "secret-a", UpdatedAt: 2}},
		Snippets: []SyncBundleSnippet{{ID: snippetID, GroupID: &groupID, Name: "snippet-a", Body: "echo a", CreatedAt: 1, UpdatedAt: 2}},
	}
	if _, err := instance.service.ImportBundle(ctxA, ImportBundleRequest{Bundle: bundle}); err != nil {
		t.Fatal(err)
	}

	digestA, err := instance.service.Digest(ctxA)
	if err != nil || len(digestA.Assets) != 1 || digestA.Assets[0].ID != assetID {
		t.Fatalf("user A digest = %+v, err=%v", digestA, err)
	}
	digestB, err := instance.service.Digest(ctxB)
	if err != nil || len(digestB.Assets) != 0 {
		t.Fatalf("user B digest contains user A's asset = %+v, err=%v", digestB, err)
	}
	if _, err := instance.db.GroupGet(ctxB, groupID); err == nil {
		t.Fatal("user B read imported group from user A")
	}
	if _, err := instance.db.CredentialGetRow(ctxB, credentialID); err == nil {
		t.Fatal("user B read imported credential from user A")
	}
	if _, err := instance.db.SnippetGet(ctxB, snippetID); err == nil {
		t.Fatal("user B read imported snippet from user A")
	}
	if _, err := instance.db.AssetGet(ctxA, assetID); err != nil {
		t.Fatalf("user A cannot read imported asset: %v", err)
	}

	payloadA := knownHostObject{ID: ids.New(), Host: "shared.example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:a", AddedAt: 1}
	payloadB := knownHostObject{ID: ids.New(), Host: "shared.example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:b", AddedAt: 2}
	if err := instance.service.engine.knownHostUpsert(ctxA, payloadA, ""); err != nil {
		t.Fatal(err)
	}
	if err := instance.service.engine.knownHostUpsert(ctxB, payloadB, ""); err != nil {
		t.Fatal(err)
	}
	knownA, foundA, err := instance.db.KnownHostGet(ctxA, payloadA.Host, payloadA.Port, payloadA.KeyType)
	if err != nil || !foundA || knownA.ID != payloadA.ID {
		t.Fatalf("user A known host = %+v, found=%v, err=%v", knownA, foundA, err)
	}
	knownB, foundB, err := instance.db.KnownHostGet(ctxB, payloadB.Host, payloadB.Port, payloadB.KeyType)
	if err != nil || !foundB || knownB.ID != payloadB.ID {
		t.Fatalf("user B known host = %+v, found=%v, err=%v", knownB, foundB, err)
	}
	if err := instance.db.KnownHostRemove(ctxB, payloadA.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := instance.db.KnownHostGet(ctxA, payloadA.Host, payloadA.Port, payloadA.KeyType); err != nil || !found {
		t.Fatalf("user A known host changed by user B: found=%v, err=%v", found, err)
	}

	stateA := aiProfilesState{Version: aiProfilesStoreVersion, Profiles: []aiProfileRecord{{ID: "profile-a", Name: "A"}}}
	stateB := aiProfilesState{Version: aiProfilesStoreVersion, Profiles: []aiProfileRecord{{ID: "profile-b", Name: "B"}}}
	if err := instance.service.engine.aiProfilesSave(ctxA, stateA, 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := instance.service.engine.aiProfilesSave(ctxB, stateB, 2, ""); err != nil {
		t.Fatal(err)
	}
	loadedA, _, found, err := instance.service.engine.aiProfilesLoad(ctxA)
	if err != nil || !found || len(loadedA.Profiles) != 1 || loadedA.Profiles[0].ID != "profile-a" {
		t.Fatalf("user A AI profiles = %+v, found=%v, err=%v", loadedA, found, err)
	}
	loadedB, _, found, err := instance.service.engine.aiProfilesLoad(ctxB)
	if err != nil || !found || len(loadedB.Profiles) != 1 || loadedB.Profiles[0].ID != "profile-b" {
		t.Fatalf("user B AI profiles = %+v, found=%v, err=%v", loadedB, found, err)
	}

	if err := instance.service.engine.syncTombstonePut(ctxA, assetID, KindAsset, 10); err != nil {
		t.Fatal(err)
	}
	if _, found, err := instance.service.engine.syncTombstoneGet(ctxB, assetID); err != nil || found {
		t.Fatalf("user B read user A's tombstone: found=%v, err=%v", found, err)
	}
	if err := instance.service.engine.syncTombstonePut(ctxB, assetID, KindAsset, 20); err != nil {
		t.Fatal(err)
	}
	tombstoneB, found, err := instance.service.engine.syncTombstoneGet(ctxB, assetID)
	if err != nil || !found || tombstoneB.DeletedAt != 20 {
		t.Fatalf("user B tombstone = %+v, found=%v, err=%v", tombstoneB, found, err)
	}
	tombstoneA, found, err := instance.service.engine.syncTombstoneGet(ctxA, assetID)
	if err != nil || !found || tombstoneA.DeletedAt != 10 {
		t.Fatalf("user A tombstone = %+v, found=%v, err=%v", tombstoneA, found, err)
	}
	if _, err := instance.db.AssetGet(ctxA, assetID); err != nil {
		t.Fatalf("user A's asset changed by tombstone from another scope: %v", err)
	}
}
