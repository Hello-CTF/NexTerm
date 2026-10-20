package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestKnownHostMigrationReadDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "migration-owner-a")
	ownerB := ipc.WithUserID(ctx, "migration-owner-b")
	for _, acceptance := range []struct {
		ctx         context.Context
		fingerprint string
	}{
		{ctx: ctx, fingerprint: "SHA256:global"},
		{ctx: ownerA, fingerprint: "SHA256:owner-a"},
		{ctx: ownerB, fingerprint: "SHA256:owner-b"},
	} {
		if err := database.KnownHostAccept(acceptance.ctx, "migration.test", 22, "ssh-ed25519", acceptance.fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := database.KnownHostListForMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("migration rows = %+v, want global and both owners", rows)
	}
	seen := make(map[string]string, len(rows))
	for _, row := range rows {
		seen[row.OwnerID] = row.Fingerprint
	}
	for ownerID, fingerprint := range map[string]string{"": "SHA256:global", "migration-owner-a": "SHA256:owner-a", "migration-owner-b": "SHA256:owner-b"} {
		if seen[ownerID] != fingerprint {
			t.Fatalf("migration owner %q fingerprint = %q, want %q; rows=%+v", ownerID, seen[ownerID], fingerprint, rows)
		}
	}
	for _, ownerCtx := range []context.Context{ownerA, ownerB} {
		owned, err := database.KnownHostList(ownerCtx)
		if err != nil || len(owned) != 1 || owned[0].OwnerID != ipc.OwnerID(ownerCtx) {
			t.Fatalf("ordinary owner list crossed its boundary: %+v, %v", owned, err)
		}
	}
}

func TestKnownHostOwnerIsolationDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "isolation-owner-a")
	ownerB := ipc.WithUserID(ctx, "isolation-owner-b")
	if err := database.KnownHostAccept(ownerA, "isolation.test", 22, "ssh-ed25519", "SHA256:a"); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostAccept(ownerB, "isolation.test", 22, "ssh-ed25519", "SHA256:b"); err != nil {
		t.Fatal(err)
	}
	rowA, foundA, err := database.KnownHostGet(ownerA, "isolation.test", 22, "ssh-ed25519")
	if err != nil || !foundA || rowA.Fingerprint != "SHA256:a" || rowA.OwnerID != "isolation-owner-a" {
		t.Fatalf("owner A row = %+v, %v, %v", rowA, foundA, err)
	}
	rowB, foundB, err := database.KnownHostGet(ownerB, "isolation.test", 22, "ssh-ed25519")
	if err != nil || !foundB || rowB.Fingerprint != "SHA256:b" || rowB.OwnerID != "isolation-owner-b" {
		t.Fatalf("owner B row = %+v, %v, %v", rowB, foundB, err)
	}
	// An accept upsert replaces only the caller's own row.
	if err := database.KnownHostAccept(ownerA, "isolation.test", 22, "ssh-ed25519", "SHA256:a2"); err != nil {
		t.Fatal(err)
	}
	rowA, _, _ = database.KnownHostGet(ownerA, "isolation.test", 22, "ssh-ed25519")
	rowB, _, _ = database.KnownHostGet(ownerB, "isolation.test", 22, "ssh-ed25519")
	if rowA.Fingerprint != "SHA256:a2" || rowB.Fingerprint != "SHA256:b" {
		t.Fatalf("accept upsert crossed owners: A=%+v B=%+v", rowA, rowB)
	}
	// A removal carrying the other owner's ID deletes nothing.
	if err := database.KnownHostRemove(ownerA, rowB.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ownerB, "isolation.test", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("cross-owner remove deleted owner B: found=%v err=%v", found, err)
	}
	if err := database.KnownHostRemove(ownerA, rowA.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "isolation.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("owner A row survived its own removal: found=%v err=%v", found, err)
	}
	var tombstones int64
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", rowA.ID, "isolation-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("owner A removal tombstones = %d, want 1", tombstones)
	}
}

func TestKnownHostCompensateAcceptDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "compensate-owner-a")
	ownerB := ipc.WithUserID(ctx, "compensate-owner-b")
	// A failed mirror write after an insert removes the freshly inserted row.
	if err := database.KnownHostAccept(ownerA, "insert.test", 22, "ssh-ed25519", "SHA256:insert"); err != nil {
		t.Fatal(err)
	}
	inserted, found, err := database.KnownHostGet(ownerA, "insert.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("insert accept = %+v, %v, %v", inserted, found, err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "insert.test", 22, "ssh-ed25519", inserted, nil); err != nil || !applied {
		t.Fatalf("insert compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "insert.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("compensated insert left a ledger row: found=%v err=%v", found, err)
	}
	// A failed mirror write after a replacement restores the prior fingerprint and revision.
	if err := database.KnownHostAcceptWithKey(ownerA, "replace.test", 22, "ssh-ed25519", "SHA256:old", "b2xkLWtleQ=="); err != nil {
		t.Fatal(err)
	}
	previous, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("previous row = %+v, %v, %v", previous, found, err)
	}
	if previous.Key != "b2xkLWtleQ==" {
		t.Fatalf("previous row lost its key material: %+v", previous)
	}
	if err := database.KnownHostAcceptWithKey(ownerA, "replace.test", 22, "ssh-ed25519", "SHA256:new", "bmV3LWtleQ=="); err != nil {
		t.Fatal(err)
	}
	accepted, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("replacement accept = %+v, %v, %v", accepted, found, err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "replace.test", 22, "ssh-ed25519", accepted, &previous); err != nil || !applied {
		t.Fatalf("replacement compensation applied=%v err=%v", applied, err)
	}
	restored, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("restored row = %+v, %v, %v", restored, found, err)
	}
	if restored.ID != previous.ID || restored.Fingerprint != previous.Fingerprint || restored.Key != previous.Key || restored.AddedAt != previous.AddedAt {
		t.Fatalf("compensated replacement = %+v, want %+v", restored, previous)
	}
	// Compensation is owner-scoped: another owner cannot roll back someone else's accept.
	if applied, err := database.KnownHostCompensateAccept(ownerB, "replace.test", 22, "ssh-ed25519", restored, nil); err != nil || applied {
		t.Fatalf("cross-owner compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("cross-owner compensation deleted owner A: found=%v err=%v", found, err)
	}
}

func TestKnownHostCompensateAcceptSkipsNewerDecisionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "cas-owner-a")
	// An interleaved independent accept between the original accept and its compensation must survive:
	// the CAS guard never rolls back a newer trust decision.
	if err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:first", "Zmlyc3Qta2V5"); err != nil {
		t.Fatal(err)
	}
	first, found, err := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("first accept = %+v, %v, %v", first, found, err)
	}
	if err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:newer", "bmV3ZXIta2V5"); err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "interleave.test", 22, "ssh-ed25519", first, nil); err != nil || applied {
		t.Fatalf("interleaved compensation applied=%v err=%v", applied, err)
	}
	row, found, err := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("newer decision = %+v, %v, %v", row, found, err)
	}
	if row.Fingerprint != "SHA256:newer" || row.Key != "bmV3ZXIta2V5" {
		t.Fatalf("compensation rolled back a newer decision: %+v", row)
	}
	// The same CAS discipline applies to replacements: a stale accepted state must not restore the previous row.
	previous := row
	if err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:third", "dGhpcmQta2V5"); err != nil {
		t.Fatal(err)
	}
	third, _, _ := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519")
	if err := database.KnownHostAcceptWithKey(ownerA, "interleave.test", 22, "ssh-ed25519", "SHA256:fourth", "Zm91cnRoLWtleQ=="); err != nil {
		t.Fatal(err)
	}
	if applied, err := database.KnownHostCompensateAccept(ownerA, "interleave.test", 22, "ssh-ed25519", third, &previous); err != nil || applied {
		t.Fatalf("stale replacement compensation applied=%v err=%v", applied, err)
	}
	if row, _, _ := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:fourth" || row.Key != "Zm91cnRoLWtleQ==" {
		t.Fatalf("stale replacement compensation changed the row: %+v", row)
	}
	// Compensating with the exact current state still applies.
	fourth, _, _ := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519")
	if applied, err := database.KnownHostCompensateAccept(ownerA, "interleave.test", 22, "ssh-ed25519", fourth, nil); err != nil || !applied {
		t.Fatalf("current-state compensation applied=%v err=%v", applied, err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "interleave.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("current-state compensation left a row: found=%v err=%v", found, err)
	}
}

func TestKnownHostRestoreRemovedKeepsNewerDecisionDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "guard-owner-a")
	if err := database.KnownHostAcceptWithKey(ownerA, "guard.test", 22, "ssh-ed25519", "SHA256:old", "b2xkLWtleQ=="); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "guard.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed row = %+v, %v, %v", row, found, err)
	}
	removed, found, err := database.KnownHostRemoveForMirror(ownerA, row.ID)
	if err != nil || !found {
		t.Fatalf("removal = %+v, %v, %v", removed, found, err)
	}
	// An independent accept lands before the failed mirror rewrite is compensated.
	if err := database.KnownHostAcceptWithKey(ownerA, "guard.test", 22, "ssh-ed25519", "SHA256:newer", "bmV3ZXIta2V5"); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
		t.Fatal(err)
	}
	current, found, err := database.KnownHostGet(ownerA, "guard.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("newer decision = %+v, %v, %v", current, found, err)
	}
	if current.Fingerprint != "SHA256:newer" || current.Key != "bmV3ZXIta2V5" {
		t.Fatalf("restore rolled back a newer decision: %+v", current)
	}
	// The tombstone of the removed row stands so the deletion still converges.
	var tombstones int64
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", row.ID, "guard-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("removed row tombstones = %d, want 1", tombstones)
	}
}

func TestKnownHostRemoveForMirrorRestoreDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "mirror-owner-a")
	ownerB := ipc.WithUserID(ctx, "mirror-owner-b")
	if err := database.KnownHostAccept(ownerA, "mirror.test", 22, "ssh-ed25519", "SHA256:mirror"); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("seed row = %+v, %v, %v", row, found, err)
	}
	// The removal is owner-scoped: another owner neither sees nor removes the row.
	if _, found, err := database.KnownHostRemoveForMirror(ownerB, row.ID); err != nil || found {
		t.Fatalf("cross-owner removal found=%v err=%v", found, err)
	}
	removed, found, err := database.KnownHostRemoveForMirror(ownerA, row.ID)
	if err != nil || !found {
		t.Fatalf("removal = %+v, %v, %v", removed, found, err)
	}
	if removed != row {
		t.Fatalf("removed row = %+v, want %+v", removed, row)
	}
	if _, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("removed row still present: found=%v err=%v", found, err)
	}
	var tombstones int64
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", row.ID, "mirror-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("removal tombstones = %d, want 1", tombstones)
	}
	// Restoring after a failed mirror rewrite brings the exact row back and drops the tombstone.
	if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
		t.Fatal(err)
	}
	restored, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("restored row = %+v, %v, %v", restored, found, err)
	}
	if restored != row {
		t.Fatalf("restored row = %+v, want %+v", restored, row)
	}
	if err := database.GORM().WithContext(ctx).Table("sync_tombstone").
		Where("id = ? AND owner_id = ?", row.ID, "mirror-owner-a").Count(&tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("restore left %d tombstones, want 0", tombstones)
	}
	// With opt-in disabled the removal writes no tombstone and the restore still round-trips.
	if err := database.SetKnownHostSyncOptIn(ownerA, "mirror-owner-a", false); err != nil {
		t.Fatal(err)
	}
	removed, found, err = database.KnownHostRemoveForMirror(ownerA, row.ID)
	if err != nil || !found {
		t.Fatalf("opt-out removal = %+v, %v, %v", removed, found, err)
	}
	if err := database.KnownHostRestoreRemoved(ownerA, removed); err != nil {
		t.Fatal(err)
	}
	if restored, found, err := database.KnownHostGet(ownerA, "mirror.test", 22, "ssh-ed25519"); err != nil || !found || restored != row {
		t.Fatalf("opt-out restored row = %+v, %v, %v", restored, found, err)
	}
}

func TestKnownHostKeyMaterialDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := context.Background()
	ownerA := ipc.WithUserID(ctx, "material-owner-a")
	ownerB := ipc.WithUserID(ctx, "material-owner-b")
	// Accepting with complete key material stores it on the ledger row.
	if err := database.KnownHostAcceptWithKey(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:material", "bWF0ZXJpYWwta2V5"); err != nil {
		t.Fatal(err)
	}
	row, found, err := database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519")
	if err != nil || !found || row.Key != "bWF0ZXJpYWwta2V5" {
		t.Fatalf("key material row = %+v, %v, %v", row, found, err)
	}
	// A fingerprint-only re-accept keeps the recorded material.
	if err := database.KnownHostAccept(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:material"); err != nil {
		t.Fatal(err)
	}
	if row, _, _ = database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519"); row.Key != "bWF0ZXJpYWwta2V5" {
		t.Fatalf("fingerprint-only re-accept lost material: %+v", row)
	}
	// A replacement with new material updates fingerprint and key together.
	if err := database.KnownHostAcceptWithKey(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:rotated", "cm90YXRlZC1rZXk="); err != nil {
		t.Fatal(err)
	}
	if row, _, _ = database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:rotated" || row.Key != "cm90YXRlZC1rZXk=" {
		t.Fatalf("material replacement = %+v", row)
	}
	// A fingerprint-only replacement clears stale material: the old key must not verify under the new fingerprint.
	if err := database.KnownHostAccept(ownerA, "material.test", 22, "ssh-ed25519", "SHA256:cleared"); err != nil {
		t.Fatal(err)
	}
	if row, _, _ = database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519"); row.Fingerprint != "SHA256:cleared" || row.Key != "" {
		t.Fatalf("fingerprint-only replacement kept stale material: %+v", row)
	}
	// Key material is owner-scoped: owners hold independent material for the same endpoint.
	if err := database.KnownHostAcceptWithKey(ownerB, "material.test", 22, "ssh-ed25519", "SHA256:material-b", "b3duZXItYi1rZXk="); err != nil {
		t.Fatal(err)
	}
	rowA, _, _ := database.KnownHostGet(ownerA, "material.test", 22, "ssh-ed25519")
	rowB, _, _ := database.KnownHostGet(ownerB, "material.test", 22, "ssh-ed25519")
	if rowA.Fingerprint != "SHA256:cleared" || rowA.Key != "" || rowB.Fingerprint != "SHA256:material-b" || rowB.Key != "b3duZXItYi1rZXk=" {
		t.Fatalf("owner materials crossed: A=%+v B=%+v", rowA, rowB)
	}
}
