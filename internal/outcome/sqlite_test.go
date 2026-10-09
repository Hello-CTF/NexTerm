package outcome_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func openMigratedStore(t *testing.T) (*store.Store, *outcome.SQLiteStore) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	var version int64
	if err := storage.DB().QueryRowContext(context.Background(), `SELECT version FROM schema_migrations WHERE version = 4`).Scan(&version); err != nil {
		t.Fatalf("migration 0004 not applied: %v", err)
	}
	sqliteStore, err := outcome.NewSQLiteStore(storage.DB())
	if err != nil {
		t.Fatal(err)
	}
	return storage, sqliteStore
}

func fixedTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func pendingRecord(key string) outcome.Record {
	return outcome.Record{
		IdempotenceKey:     key,
		AuthorizationID:    "guard:authorize-1",
		Kind:               outcome.KindCommand,
		CanonicalArguments: []byte(`{"command":"apply"}`),
		State:              outcome.ExecutionPending,
		Outcome:            outcome.OutcomeNotAttempted,
		Audit:              outcome.Audit{State: outcome.AuditPending},
		Revision:           1,
		CreatedAt:          fixedTime(1000),
	}
}

func TestSQLiteStoreReserveAndGetRoundTrip(t *testing.T) {
	_, sqliteStore := openMigratedStore(t)
	ctx := context.Background()
	record := pendingRecord("round-trip")
	stored, reserved, err := sqliteStore.Reserve(ctx, record)
	if err != nil || !reserved {
		t.Fatalf("reserve: reserved=%v err=%v", reserved, err)
	}
	if !reflect.DeepEqual(stored, record) {
		t.Fatalf("reserved = %+v, want %+v", stored, record)
	}
	loaded, err := sqliteStore.Get(ctx, record.IdempotenceKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, record) {
		t.Fatalf("Get = %+v, want %+v", loaded, record)
	}
	if _, err := sqliteStore.Get(ctx, "missing"); !errors.Is(err, outcome.ErrNotFound) {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestSQLiteStoreReserveDuplicateReturnsStored(t *testing.T) {
	_, sqliteStore := openMigratedStore(t)
	ctx := context.Background()
	record := pendingRecord("duplicate")
	if _, reserved, err := sqliteStore.Reserve(ctx, record); err != nil || !reserved {
		t.Fatalf("first reserve: reserved=%v err=%v", reserved, err)
	}
	conflicting := pendingRecord("duplicate")
	conflicting.AuthorizationID = "guard:authorize-2"
	conflicting.CanonicalArguments = []byte(`{"command":"other"}`)
	stored, reserved, err := sqliteStore.Reserve(ctx, conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if reserved {
		t.Fatal("duplicate key was reserved twice")
	}
	if stored.AuthorizationID != record.AuthorizationID || string(stored.CanonicalArguments) != string(record.CanonicalArguments) {
		t.Fatalf("duplicate reserve returned %+v, want the original record", stored)
	}
}

func TestSQLiteStoreUpdateCompareAndSwap(t *testing.T) {
	_, sqliteStore := openMigratedStore(t)
	ctx := context.Background()
	record := pendingRecord("cas")
	if _, _, err := sqliteStore.Reserve(ctx, record); err != nil {
		t.Fatal(err)
	}

	running := record
	running.State = outcome.ExecutionRunning
	running.Outcome = outcome.OutcomeUnknown
	started := fixedTime(2000)
	running.StartedAt = &started
	running.Revision = 2
	if err := sqliteStore.Update(ctx, running, 1); err != nil {
		t.Fatalf("first CAS: %v", err)
	}
	stale := running
	stale.Revision = 3
	if err := sqliteStore.Update(ctx, stale, 1); !errors.Is(err, outcome.ErrRevisionConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	badRevision := running
	badRevision.Revision = 5
	if err := sqliteStore.Update(ctx, badRevision, 3); !errors.Is(err, outcome.ErrRevisionConflict) {
		t.Fatalf("non-sequential revision error = %v", err)
	}

	finished := running
	finished.State = outcome.ExecutionFinished
	finished.Outcome = outcome.OutcomeAccepted
	code := 0
	finished.Result = outcome.ExitResult{ExitCode: &code}
	finished.Audit = outcome.Audit{State: outcome.AuditPersisted}
	completed := fixedTime(3000)
	finished.Audit.CompletedAt = &completed
	finishedAt := fixedTime(2900)
	finished.FinishedAt = &finishedAt
	finished.Revision = 3
	if err := sqliteStore.Update(ctx, finished, 2); err != nil {
		t.Fatalf("second CAS: %v", err)
	}
	loaded, err := sqliteStore.Get(ctx, record.IdempotenceKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, finished) {
		t.Fatalf("Get = %+v, want %+v", loaded, finished)
	}

	missing := pendingRecord("cas-missing")
	missing.Revision = 2
	if err := sqliteStore.Update(ctx, missing, 1); !errors.Is(err, outcome.ErrNotFound) {
		t.Fatalf("missing key update error = %v", err)
	}
}

func TestSQLiteStorePreservesCanonicalArgumentsAndAuditFailure(t *testing.T) {
	_, sqliteStore := openMigratedStore(t)
	ctx := context.Background()
	record := pendingRecord("canonical")
	record.CanonicalArguments = []byte(`{"a":1.00,"b":[2,3],"c":{"d":null}}`)
	record.State = outcome.ExecutionFinished
	record.Outcome = outcome.OutcomeUnknown
	record.Result = outcome.ExitResult{Error: "connection lost after write"}
	record.Audit = outcome.Audit{State: outcome.AuditFailed, Error: "audit storage unavailable"}
	completed := fixedTime(4200)
	record.Audit.CompletedAt = &completed
	finished := fixedTime(4100)
	record.FinishedAt = &finished
	if _, _, err := sqliteStore.Reserve(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := sqliteStore.Get(ctx, record.IdempotenceKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, record) {
		t.Fatalf("Get = %+v, want %+v", loaded, record)
	}
	if string(loaded.CanonicalArguments) != string(record.CanonicalArguments) {
		t.Fatalf("canonical arguments changed: %s", loaded.CanonicalArguments)
	}
}

func TestSQLiteStoreLedgerEndToEnd(t *testing.T) {
	storage, sqliteStore := openMigratedStore(t)
	ctx := context.Background()
	ledger, err := outcome.New(outcome.Options{Store: sqliteStore, Auditor: storage.OutcomeAuditor()})
	if err != nil {
		t.Fatal(err)
	}
	request := outcome.Request{
		IdempotenceKey:  "ledger-e2e",
		AuthorizationID: "guard:authorize-1",
		Kind:            outcome.KindFile,
		Arguments:       []byte(`{"path":"/tmp/a","content":"x"}`),
	}
	effectCalls := 0
	record, err := ledger.Execute(ctx, request, func(context.Context) (outcome.Completion, error) {
		effectCalls++
		return outcome.Completion{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != outcome.OutcomeAccepted || record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("record = %+v", record)
	}
	stored, err := sqliteStore.Get(ctx, request.IdempotenceKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != record.Revision || stored.Outcome != record.Outcome || stored.Audit.State != record.Audit.State {
		t.Fatalf("stored = %+v, returned = %+v", stored, record)
	}
	kind := store.OutcomeAuditKind
	rows, err := storage.AuditQuery(ctx, store.AuditQuery{Kind: &kind, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d", len(rows))
	}
	if rows[0].Source != "ai" || rows[0].Kind != store.OutcomeAuditKind {
		t.Fatalf("audit row = %+v", rows[0])
	}

	duplicate, err := ledger.Execute(ctx, request, func(context.Context) (outcome.Completion, error) {
		effectCalls++
		return outcome.Completion{}, nil
	})
	if err != nil {
		t.Fatalf("duplicate error = %v", err)
	}
	if duplicate.Revision != record.Revision || effectCalls != 1 {
		t.Fatalf("duplicate re-ran work: revision=%d effects=%d", duplicate.Revision, effectCalls)
	}
	rows, err = storage.AuditQuery(ctx, store.AuditQuery{Kind: &kind, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("duplicate appended another audit row: %d", len(rows))
	}
}
