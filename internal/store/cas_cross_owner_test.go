package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A-04: direct CredentialUpdateCAS cross-owner behavior — one owner's
// credentials can never be updated, overwritten, or deleted by another owner,
// while missing, legacy, and same-owner paths keep their existing semantics.

func credentialCASFixture(t *testing.T) *store.Store {
	t.Helper()
	return dbtest.NewFixture(t).OpenStore(t)
}

func createCredentialFor(t *testing.T, db *store.Store, ctx context.Context, name string) store.CredentialRow {
	t.Helper()
	id, err := db.CredentialPut(ctx, store.CredentialInput{Name: name, Kind: "private_key", Nonce: []byte{1}, Blob: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.CredentialGetRow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCredentialUpdateCASSameOwnerSucceeds(t *testing.T) {
	db := credentialCASFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	before := createCredentialFor(t, db, ctx, "key-a")

	if err := db.CredentialUpdateCAS(ctx, db.GORM(), before.ID, before.UpdatedAt, store.CredentialInput{
		Name: "key-a2", Kind: "private_key", Nonce: []byte{3}, Blob: []byte{4},
	}); err != nil {
		t.Fatalf("same-owner CAS: %v", err)
	}
	after, err := db.CredentialGetRow(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "key-a2" || after.UpdatedAt <= before.UpdatedAt || !bytes.Equal(after.Blob, []byte{4}) {
		t.Fatalf("after = %+v", after)
	}
}

func TestCredentialUpdateCASStaleExpectedConflicts(t *testing.T) {
	db := credentialCASFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	before := createCredentialFor(t, db, ctx, "key-a")
	if err := db.CredentialUpdateCAS(ctx, db.GORM(), before.ID, before.UpdatedAt, store.CredentialInput{
		Name: "key-a2", Kind: "private_key", Nonce: []byte{3}, Blob: []byte{4},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := db.CredentialGetRow(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.CredentialUpdateCAS(ctx, db.GORM(), before.ID, before.UpdatedAt, store.CredentialInput{
		Name: "key-a3", Kind: "private_key", Nonce: []byte{5}, Blob: []byte{6},
	}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("stale CAS error = %v, want ErrWriteConflict", err)
	}
	retained, err := db.CredentialGetRow(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Name != "key-a2" || retained.UpdatedAt != updated.UpdatedAt {
		t.Fatalf("credential after stale CAS = %+v, want %+v", retained, updated)
	}
}

func TestCredentialUpdateCASMissingConflicts(t *testing.T) {
	db := credentialCASFixture(t)
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	if err := db.CredentialUpdateCAS(ctx, db.GORM(), "01JEXAMPLEEXAMPLEEXAMPLEEX", 1, store.CredentialInput{
		Name: "ghost", Kind: "private_key", Nonce: []byte{1}, Blob: []byte{2},
	}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("missing CAS error = %v, want ErrWriteConflict", err)
	}
}

func TestCredentialUpdateCASCrossOwnerConflict(t *testing.T) {
	db := credentialCASFixture(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	before := createCredentialFor(t, db, ctxA, "key-a")

	if err := db.CredentialUpdateCAS(ctxB, db.GORM(), before.ID, before.UpdatedAt, store.CredentialInput{
		Name: "stolen", Kind: "private_key", Nonce: []byte{9}, Blob: []byte{9},
	}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner CAS error = %v, want ErrWriteConflict", err)
	}
	retained, err := db.CredentialGetRow(ctxA, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Name != "key-a" || !bytes.Equal(retained.Blob, []byte{2}) || retained.UpdatedAt != before.UpdatedAt {
		t.Fatalf("credential after cross-owner CAS = %+v, want %+v", retained, before)
	}
	if _, err := db.CredentialGetRow(ctxB, before.ID); err == nil {
		t.Fatal("cross-owner read exposed the credential")
	}
}

func TestCredentialPutCrossOwnerConflict(t *testing.T) {
	db := credentialCASFixture(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	before := createCredentialFor(t, db, ctxA, "key-a")

	if _, err := db.CredentialPut(ctxB, store.CredentialInput{
		ID: before.ID, Name: "replaced", Kind: "private_key", Nonce: []byte{9}, Blob: []byte{9},
	}); !errors.Is(err, store.ErrWriteConflict) {
		t.Fatalf("cross-owner put error = %v, want ErrWriteConflict", err)
	}
	retained, err := db.CredentialGetRow(ctxA, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Name != "key-a" || !bytes.Equal(retained.Blob, []byte{2}) || retained.UpdatedAt != before.UpdatedAt {
		t.Fatalf("credential after cross-owner put = %+v, want %+v", retained, before)
	}
}

func TestCredentialDeleteCrossOwnerSurvives(t *testing.T) {
	db := credentialCASFixture(t)
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	before := createCredentialFor(t, db, ctxA, "key-a")

	if err := db.CredentialDelete(ctxB, before.ID); err != nil {
		t.Fatalf("cross-owner delete: %v", err)
	}
	retained, err := db.CredentialGetRow(ctxA, before.ID)
	if err != nil {
		t.Fatalf("credential deleted by another owner: %v", err)
	}
	if retained.Name != "key-a" || !bytes.Equal(retained.Blob, []byte{2}) {
		t.Fatalf("credential after cross-owner delete = %+v", retained)
	}
}
