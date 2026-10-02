package outcome

import (
	"context"
	"errors"
	"testing"
)

func TestTerminalPersistenceFailurePreventsAuditAndDuplicateExecution(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("terminal-persistence-failure")
	persistErr := errors.New("terminal write failed")
	store.updateError = func(record Record) error {
		if record.State == ExecutionFinished && record.Audit.State == AuditPending {
			return persistErr
		}
		return nil
	}
	effectCalls := 0

	record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v", err)
	}
	if record.Outcome != OutcomeAccepted || record.Audit.State != AuditPending {
		t.Fatalf("returned record = %+v", record)
	}
	stored := store.snapshot(t, request.IdempotenceKey)
	if stored.State != ExecutionRunning || stored.Outcome != OutcomeUnknown {
		t.Fatalf("failed terminal write did not retain conservative state: %+v", stored)
	}
	if len(auditor.snapshots()) != 0 {
		t.Fatal("unpersisted terminal state was audited as final")
	}

	if _, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	}); !errors.Is(err, ErrInProgress) {
		t.Fatalf("duplicate error = %v", err)
	}
	if effectCalls != 1 {
		t.Fatalf("effect calls = %d", effectCalls)
	}
}

func TestAuditStatusPersistenceFailureIsReported(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("audit-status-persistence-failure")
	persistErr := errors.New("audit status write failed")
	store.updateError = func(record Record) error {
		if record.State == ExecutionFinished && record.Audit.State != AuditPending {
			return persistErr
		}
		return nil
	}

	record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		return Completion{}, nil
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v", err)
	}
	if record.Outcome != OutcomeAccepted || record.Audit.State != AuditPersisted {
		t.Fatalf("actual audit result was lost: %+v", record)
	}
	stored := store.snapshot(t, request.IdempotenceKey)
	if stored.Outcome != OutcomeAccepted || stored.Audit.State != AuditPending {
		t.Fatalf("stored record = %+v", stored)
	}
	if len(auditor.snapshots()) != 1 {
		t.Fatalf("audit attempts = %d", len(auditor.snapshots()))
	}
}

func TestRunningPersistenceFailureDoesNotInvokeEffect(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	persistErr := errors.New("running write failed")
	store.updateError = func(record Record) error {
		if record.State == ExecutionRunning {
			return persistErr
		}
		return nil
	}
	effectCalls := 0

	_, err := ledger.Execute(context.Background(), testRequest("running-persistence-failure"), func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v", err)
	}
	if effectCalls != 0 || len(auditor.snapshots()) != 0 {
		t.Fatalf("unreserved effect ran: effects=%d audits=%d", effectCalls, len(auditor.snapshots()))
	}
}
