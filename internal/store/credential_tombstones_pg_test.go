package store_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
)

func TestCredentialTombstonePutPostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)

	if err := db.CredentialTombstonePut(ctx, "cred-pg-tombstone", 200); err != nil {
		t.Fatal(err)
	}
	if err := db.CredentialTombstonePut(ctx, "cred-pg-tombstone", 100); err != nil {
		t.Fatal(err)
	}
	tombstone, err := db.CredentialTombstoneGet(ctx, "cred-pg-tombstone")
	if err != nil || tombstone.DeletedAt != 200 {
		t.Fatalf("older put must not regress revision: tombstone=%+v err=%v", tombstone, err)
	}
	if err := db.CredentialTombstonePut(ctx, "cred-pg-tombstone", 300); err != nil {
		t.Fatal(err)
	}
	if tombstone, err = db.CredentialTombstoneGet(ctx, "cred-pg-tombstone"); err != nil || tombstone.DeletedAt != 300 {
		t.Fatalf("newer put must advance revision: tombstone=%+v err=%v", tombstone, err)
	}
}
