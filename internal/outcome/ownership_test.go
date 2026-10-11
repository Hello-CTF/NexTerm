package outcome_test

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
)

func TestSQLiteStoreScopesIdempotenceToUser(t *testing.T) {
	_, sqliteStore := openMigratedStore(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	recordA := pendingRecord("shared-key")
	if _, reserved, err := sqliteStore.Reserve(ctxA, recordA); err != nil || !reserved {
		t.Fatalf("user A reserve = %v, err=%v", reserved, err)
	}
	recordB := pendingRecord("shared-key")
	recordB.AuthorizationID = "guard:authorize-b"
	if _, reserved, err := sqliteStore.Reserve(ctxB, recordB); err != nil || !reserved {
		t.Fatalf("user B reserve = %v, err=%v", reserved, err)
	}
	loadedA, err := sqliteStore.Get(ctxA, recordA.IdempotenceKey)
	if err != nil || loadedA.AuthorizationID != recordA.AuthorizationID {
		t.Fatalf("user A record = %+v, err=%v", loadedA, err)
	}
	loadedB, err := sqliteStore.Get(ctxB, recordB.IdempotenceKey)
	if err != nil || loadedB.AuthorizationID != recordB.AuthorizationID {
		t.Fatalf("user B record = %+v, err=%v", loadedB, err)
	}
	if _, err := sqliteStore.Get(ctxB, "missing"); err == nil || err != outcome.ErrNotFound {
		t.Fatalf("missing key error = %v", err)
	}
}
