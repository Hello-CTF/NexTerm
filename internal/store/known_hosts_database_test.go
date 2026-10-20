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
