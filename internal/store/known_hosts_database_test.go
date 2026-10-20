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
	if err := database.KnownHostCompensateAccept(ownerA, "insert.test", 22, "ssh-ed25519", nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "insert.test", 22, "ssh-ed25519"); err != nil || found {
		t.Fatalf("compensated insert left a ledger row: found=%v err=%v", found, err)
	}
	// A failed mirror write after a replacement restores the prior fingerprint and revision.
	if err := database.KnownHostAccept(ownerA, "replace.test", 22, "ssh-ed25519", "SHA256:old"); err != nil {
		t.Fatal(err)
	}
	previous, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("previous row = %+v, %v, %v", previous, found, err)
	}
	if err := database.KnownHostAccept(ownerA, "replace.test", 22, "ssh-ed25519", "SHA256:new"); err != nil {
		t.Fatal(err)
	}
	if err := database.KnownHostCompensateAccept(ownerA, "replace.test", 22, "ssh-ed25519", &previous); err != nil {
		t.Fatal(err)
	}
	restored, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519")
	if err != nil || !found {
		t.Fatalf("restored row = %+v, %v, %v", restored, found, err)
	}
	if restored.ID != previous.ID || restored.Fingerprint != previous.Fingerprint || restored.AddedAt != previous.AddedAt {
		t.Fatalf("compensated replacement = %+v, want %+v", restored, previous)
	}
	// Compensation is owner-scoped: another owner cannot roll back someone else's accept.
	if err := database.KnownHostCompensateAccept(ownerB, "replace.test", 22, "ssh-ed25519", nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.KnownHostGet(ownerA, "replace.test", 22, "ssh-ed25519"); err != nil || !found {
		t.Fatalf("cross-owner compensation deleted owner A: found=%v err=%v", found, err)
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
