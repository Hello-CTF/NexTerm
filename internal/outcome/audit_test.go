package outcome

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuditFailurePreservesExecutionOutcomeAndDoesNotRetry(t *testing.T) {
	store := newMemoryStore()
	auditErr := errors.New("audit storage unavailable")
	auditor := &memoryAuditor{err: auditErr}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("audit-failure")
	effectCalls := 0

	record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if !errors.Is(err, auditErr) {
		t.Fatalf("error = %v", err)
	}
	if record.Outcome != OutcomeAccepted || record.State != ExecutionFinished {
		t.Fatalf("audit failure changed execution outcome: %+v", record)
	}
	if record.Audit.State != AuditFailed || record.Audit.Error != auditErr.Error() || record.Audit.CompletedAt == nil {
		t.Fatalf("audit failure not recorded: %+v", record.Audit)
	}
	if stored := store.snapshot(t, request.IdempotenceKey); stored.Audit.State != AuditFailed {
		t.Fatalf("stored audit = %+v", stored.Audit)
	}

	duplicate, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if err != nil {
		t.Fatalf("duplicate error = %v", err)
	}
	if duplicate.Audit.State != AuditFailed {
		t.Fatalf("duplicate audit = %+v", duplicate.Audit)
	}
	if effectCalls != 1 || len(auditor.snapshots()) != 1 {
		t.Fatalf("duplicate retried work: effects=%d audits=%d", effectCalls, len(auditor.snapshots()))
	}
}

func TestAuditTimeoutIsBoundedAndPersisted(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	auditor.beforeAppend = func(ctx context.Context, _ Record) {
		<-ctx.Done()
	}
	ledger, err := New(Options{Store: store, Auditor: auditor, AuditTimeout: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	record, err := ledger.Execute(context.Background(), testRequest("audit-timeout"), func(context.Context) (Completion, error) {
		return Completion{}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if record.Outcome != OutcomeAccepted || record.Audit.State != AuditFailed {
		t.Fatalf("timeout outcome = %+v", record)
	}
	stored := store.snapshot(t, "audit-timeout")
	if stored.Audit.State != AuditFailed || stored.Audit.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("stored audit timeout is not inspectable: %+v", stored.Audit)
	}
}
