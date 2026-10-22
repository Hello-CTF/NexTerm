package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// A sync apply is an independent trust decision: it must invalidate the previous accept's operation
// token, so a mirror-failure compensation can never roll the sync decision back or delete it.
func TestKnownHostSyncApplyInvalidatesAcceptToken(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "user-a")

	// Insert case: A accepts with a token, then a same-ID sync apply lands with a newer revision and a
	// different fingerprint; A's compensation must not match the sync decision.
	acceptedA, previous, err := instance.db.KnownHostAcceptForMirror(ownerA, "sync-token.test", 22, "ssh-ed25519", "SHA256:local", "bG9jYWwta2V5", false)
	if err != nil || previous != nil {
		t.Fatalf("A accept = %+v, %+v, %v", acceptedA, previous, err)
	}
	result, err := instance.service.ApplyObjects(ownerA, ApplyObjectsRequest{Objects: []ApplyObject{
		applyKnownHost(t, acceptedA.ID, "sync-token.test", 22, "ssh-ed25519", "SHA256:sync", acceptedA.AddedAt+100),
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	row, found, err := instance.db.KnownHostGet(ownerA, "sync-token.test", 22, "ssh-ed25519")
	if err != nil || !found || row.Fingerprint != "SHA256:sync" || row.OpToken != "" {
		t.Fatalf("sync apply = %+v, %v, %v", row, found, err)
	}
	if applied, err := instance.db.KnownHostCompensateAccept(ownerA, "sync-token.test", 22, "ssh-ed25519", acceptedA, nil); err != nil || applied {
		t.Fatalf("compensation deleted the sync decision: applied=%v err=%v", applied, err)
	}
	if row, found, err := instance.db.KnownHostGet(ownerA, "sync-token.test", 22, "ssh-ed25519"); err != nil || !found || row.Fingerprint != "SHA256:sync" {
		t.Fatalf("sync decision lost: %+v, %v, %v", row, found, err)
	}

	// Replace case: A replaces P, then a same-ID sync apply lands; A's compensation must not restore P.
	if _, err := instance.db.KnownHostAcceptWithKey(ownerA, "sync-token-replace.test", 22, "ssh-ed25519", "SHA256:previous", "cHJldmlvdXMta2V5"); err != nil {
		t.Fatal(err)
	}
	previousRow, _, _ := instance.db.KnownHostGet(ownerA, "sync-token-replace.test", 22, "ssh-ed25519")
	acceptedR, previous, err := instance.db.KnownHostAcceptForMirror(ownerA, "sync-token-replace.test", 22, "ssh-ed25519", "SHA256:local", "bG9jYWwta2V5", true)
	if err != nil || previous == nil || previous.ID != previousRow.ID {
		t.Fatalf("replacement accept = %+v, %+v, %v", acceptedR, previous, err)
	}
	result, err = instance.service.ApplyObjects(ownerA, ApplyObjectsRequest{Objects: []ApplyObject{
		applyKnownHost(t, previousRow.ID, "sync-token-replace.test", 22, "ssh-ed25519", "SHA256:sync", acceptedR.AddedAt+100),
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if applied, err := instance.db.KnownHostCompensateAccept(ownerA, "sync-token-replace.test", 22, "ssh-ed25519", acceptedR, previous); err != nil || applied {
		t.Fatalf("compensation restored stale previous over the sync decision: applied=%v err=%v", applied, err)
	}
	if row, found, err := instance.db.KnownHostGet(ownerA, "sync-token-replace.test", 22, "ssh-ed25519"); err != nil || !found || row.Fingerprint != "SHA256:sync" {
		t.Fatalf("sync replacement decision lost: %+v, %v, %v", row, found, err)
	}
}
