package store

import (
	"context"
	"testing"
)

func TestCredentialTombstonePutKeepsMaxRevision(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if err := db.CredentialTombstonePut(ctx, "cred-tombstone", 200); err != nil {
		t.Fatal(err)
	}
	if err := db.CredentialTombstonePut(ctx, "cred-tombstone", 100); err != nil {
		t.Fatal(err)
	}
	tombstone, err := db.CredentialTombstoneGet(ctx, "cred-tombstone")
	if err != nil || tombstone.DeletedAt != 200 {
		t.Fatalf("older put must not regress revision: tombstone=%+v err=%v", tombstone, err)
	}
	if err := db.CredentialTombstonePut(ctx, "cred-tombstone", 300); err != nil {
		t.Fatal(err)
	}
	tombstone, _ = db.CredentialTombstoneGet(ctx, "cred-tombstone")
	if tombstone.DeletedAt != 300 {
		t.Fatalf("newer put must advance revision: %+v", tombstone)
	}
	if err := db.CredentialTombstoneClear(ctx, "cred-tombstone"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CredentialTombstoneGet(ctx, "cred-tombstone"); err == nil {
		t.Fatal("cleared tombstone still present")
	}
}

func TestCredentialDeleteRowLeavesTombstoneUntouched(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	put := func() {
		if _, err := db.CredentialPut(ctx, CredentialInput{
			ID: "cred-row", Name: "n", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}, KEKHint: "master:0",
		}); err != nil {
			t.Fatal(err)
		}
	}
	put()
	if err := db.CredentialDeleteRow(ctx, "cred-row"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CredentialGetRow(ctx, "cred-row"); err == nil {
		t.Fatal("row survived CredentialDeleteRow")
	}
	if _, err := db.CredentialTombstoneGet(ctx, "cred-row"); err == nil {
		t.Fatal("CredentialDeleteRow must not record a tombstone")
	}
	put()
	if err := db.CredentialDelete(ctx, "cred-row"); err != nil {
		t.Fatal(err)
	}
	tombstone, err := db.CredentialTombstoneGet(ctx, "cred-row")
	if err != nil || tombstone.DeletedAt <= 0 {
		t.Fatalf("CredentialDelete must record a tombstone: %+v err=%v", tombstone, err)
	}
}
