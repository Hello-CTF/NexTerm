package outcome

import (
	"context"
	"errors"
	"testing"
)

func TestPreCanceledExecuteRecordsNotAttempted(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("pre-canceled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	effectCalls := 0

	record, err := ledger.Execute(ctx, request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if effectCalls != 0 {
		t.Fatal("pre-canceled request invoked effect")
	}
	if record.Outcome != OutcomeNotAttempted || record.State != ExecutionFinished || record.StartedAt != nil {
		t.Fatalf("pre-canceled record = %+v", record)
	}
	if record.Audit.State != AuditPersisted || len(auditor.snapshots()) != 1 {
		t.Fatalf("pre-canceled audit = %+v, attempts = %d", record.Audit, len(auditor.snapshots()))
	}
}

func TestCancellationAfterReservationRecordsNotAttempted(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("cancel-before-running")
	ctx, cancel := context.WithCancel(context.Background())
	store.afterReserve = func(context.Context, Record) { cancel() }
	effectCalls := 0

	record, err := ledger.Execute(ctx, request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if effectCalls != 0 {
		t.Fatal("canceled reservation invoked effect")
	}
	if record.Outcome != OutcomeNotAttempted || record.StartedAt != nil || record.Audit.State != AuditPersisted {
		t.Fatalf("reservation cancellation = %+v", record)
	}
}

func TestCancellationDuringEffectUsesLiveFinalizationContexts(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("cancel-running")
	ctx, cancel := context.WithCancel(context.Background())
	store.afterUpdate = func(updateCtx context.Context, record Record) {
		if record.State == ExecutionFinished && updateCtx.Err() != nil {
			t.Errorf("terminal persistence inherited cancellation: %v", updateCtx.Err())
		}
	}
	auditor.beforeAppend = func(auditCtx context.Context, _ Record) {
		if auditCtx.Err() != nil {
			t.Errorf("audit inherited cancellation: %v", auditCtx.Err())
		}
	}

	record, err := ledger.Execute(ctx, request, func(effectCtx context.Context) (Completion, error) {
		cancel()
		<-effectCtx.Done()
		return Completion{}, effectCtx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if record.Outcome != OutcomeUnknown || record.Result.Error != context.Canceled.Error() {
		t.Fatalf("cancellation outcome = %+v", record)
	}
	if record.Audit.State != AuditPersisted || len(auditor.snapshots()) != 1 {
		t.Fatalf("audit was not finalized: %+v", record.Audit)
	}
	if stored := store.snapshot(t, request.IdempotenceKey); stored.Audit.State != AuditPersisted {
		t.Fatalf("stored audit = %+v", stored.Audit)
	}
}

func TestDefiniteResultWinsWhenEffectReturnsAfterCancellation(t *testing.T) {
	store := newMemoryStore()
	ledger := newTestLedger(t, store, &memoryAuditor{})
	ctx, cancel := context.WithCancel(context.Background())

	record, err := ledger.Execute(ctx, testRequest("definite-before-cancel"), func(context.Context) (Completion, error) {
		cancel()
		return Completion{ExitCode: intPointer(0)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != OutcomeAccepted || record.Result.ExitCode == nil || *record.Result.ExitCode != 0 {
		t.Fatalf("definite result was overwritten by cancellation: %+v", record)
	}
	if record.Audit.State != AuditPersisted {
		t.Fatalf("audit = %+v", record.Audit)
	}
}
