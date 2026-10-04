package outcome

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
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

func TestCancellationAfterLostClaimReturnsRunningRecord(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("cancel-lost-claim-running")
	ctx, cancel := context.WithCancel(context.Background())

	effectRelease := make(chan struct{})
	runningPersisted := make(chan struct{})
	store.afterUpdate = func(_ context.Context, record Record) {
		if record.State == ExecutionRunning {
			close(runningPersisted)
		}
	}
	var effectCalls atomic.Int64
	var orchestrated atomic.Bool
	bDone := make(chan error, 1)
	store.afterReserve = func(context.Context, Record) {
		if !orchestrated.CompareAndSwap(false, true) {
			return
		}
		go func() {
			_, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
				effectCalls.Add(1)
				<-effectRelease
				return Completion{}, nil
			})
			bDone <- err
		}()
		<-runningPersisted
		cancel()
	}

	canceledRecord, err := ledger.Execute(ctx, request, func(context.Context) (Completion, error) {
		effectCalls.Add(1)
		return Completion{}, nil
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrInProgress) {
		t.Fatalf("canceled caller error = %v", err)
	}
	if canceledRecord.State != ExecutionRunning || canceledRecord.Outcome != OutcomeUnknown || canceledRecord.FinishedAt != nil {
		t.Fatalf("canceled caller received unpersisted snapshot: %+v", canceledRecord)
	}
	if got := len(auditor.snapshots()); got != 0 {
		t.Fatalf("audit appended before terminal state: %d", got)
	}

	close(effectRelease)
	if err := <-bDone; err != nil {
		t.Fatalf("claimant error = %v", err)
	}
	if got := effectCalls.Load(); got != 1 {
		t.Fatalf("effect calls = %d", got)
	}
	audits := auditor.snapshots()
	if len(audits) != 1 || audits[0].Outcome != OutcomeAccepted || audits[0].State != ExecutionFinished {
		t.Fatalf("audit attempts = %+v", audits)
	}
	stored := store.snapshot(t, request.IdempotenceKey)
	if stored.Outcome != OutcomeAccepted || stored.State != ExecutionFinished || stored.Audit.State != AuditPersisted {
		t.Fatalf("final record = %+v", stored)
	}
}

func TestCancellationAfterLostClaimReturnsTerminalRecord(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("cancel-lost-claim-terminal")
	ctx, cancel := context.WithCancel(context.Background())

	terminalPersisted := make(chan struct{})
	store.afterUpdate = func(_ context.Context, record Record) {
		if record.State == ExecutionFinished && record.Audit.State == AuditPending {
			close(terminalPersisted)
		}
	}
	var effectCalls atomic.Int64
	var orchestrated atomic.Bool
	bDone := make(chan error, 1)
	store.afterReserve = func(context.Context, Record) {
		if !orchestrated.CompareAndSwap(false, true) {
			return
		}
		go func() {
			_, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
				effectCalls.Add(1)
				return Completion{}, nil
			})
			bDone <- err
		}()
		<-terminalPersisted
		cancel()
	}

	canceledRecord, err := ledger.Execute(ctx, request, func(context.Context) (Completion, error) {
		effectCalls.Add(1)
		return Completion{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller error = %v", err)
	}
	if errors.Is(err, ErrInProgress) {
		t.Fatalf("finished record reported as in progress: %v", err)
	}
	if canceledRecord.State != ExecutionFinished || canceledRecord.Outcome != OutcomeAccepted || canceledRecord.FinishedAt == nil {
		t.Fatalf("canceled caller received unpersisted snapshot: %+v", canceledRecord)
	}

	if err := <-bDone; err != nil {
		t.Fatalf("claimant error = %v", err)
	}
	if got := effectCalls.Load(); got != 1 {
		t.Fatalf("effect calls = %d", got)
	}
	audits := auditor.snapshots()
	if len(audits) != 1 || audits[0].Outcome != OutcomeAccepted || audits[0].State != ExecutionFinished {
		t.Fatalf("audit attempts = %+v", audits)
	}
	stored := store.snapshot(t, request.IdempotenceKey)
	if stored.Outcome != OutcomeAccepted || stored.State != ExecutionFinished || stored.Audit.State != AuditPersisted {
		t.Fatalf("final record = %+v", stored)
	}
}

func TestCancellationReloadFailureReturnsNoFabricatedRecord(t *testing.T) {
	getErr := errors.New("outcome store get failed")
	tests := []struct {
		name             string
		getError         func(context.Context, string) error
		timeout          time.Duration
		wantGetErr       error
		claimantFinishes bool
	}{
		{
			name:       "get error, claimant running",
			getError:   func(context.Context, string) error { return getErr },
			timeout:    time.Second,
			wantGetErr: getErr,
		},
		{
			name:             "get error, claimant terminal",
			getError:         func(context.Context, string) error { return getErr },
			timeout:          time.Second,
			wantGetErr:       getErr,
			claimantFinishes: true,
		},
		{
			name: "get deadline, claimant running",
			getError: func(ctx context.Context, _ string) error {
				<-ctx.Done()
				return ctx.Err()
			},
			timeout:    25 * time.Millisecond,
			wantGetErr: context.DeadlineExceeded,
		},
		{
			name: "get deadline, claimant terminal",
			getError: func(ctx context.Context, _ string) error {
				<-ctx.Done()
				return ctx.Err()
			},
			timeout:          25 * time.Millisecond,
			wantGetErr:       context.DeadlineExceeded,
			claimantFinishes: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			auditor := &memoryAuditor{}
			ledger, err := New(Options{Store: store, Auditor: auditor, AuditTimeout: test.timeout})
			if err != nil {
				t.Fatal(err)
			}
			request := testRequest("cancel-lost-claim-reload")
			ctx, cancel := context.WithCancel(context.Background())

			claimantState := make(chan struct{})
			store.afterUpdate = func(_ context.Context, record Record) {
				if (!test.claimantFinishes && record.State == ExecutionRunning) ||
					(test.claimantFinishes && record.State == ExecutionFinished && record.Audit.State == AuditPending) {
					close(claimantState)
				}
			}
			effectRelease := make(chan struct{})
			var effectCalls atomic.Int64
			var orchestrated atomic.Bool
			bDone := make(chan error, 1)
			store.afterReserve = func(context.Context, Record) {
				if !orchestrated.CompareAndSwap(false, true) {
					return
				}
				go func() {
					_, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
						effectCalls.Add(1)
						if !test.claimantFinishes {
							<-effectRelease
						}
						return Completion{}, nil
					})
					bDone <- err
				}()
				<-claimantState
				store.mu.Lock()
				store.getError = test.getError
				store.mu.Unlock()
				cancel()
			}

			canceledRecord, err := ledger.Execute(ctx, request, func(context.Context) (Completion, error) {
				effectCalls.Add(1)
				return Completion{}, nil
			})
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRevisionConflict) || !errors.Is(err, test.wantGetErr) {
				t.Fatalf("canceled caller error = %v", err)
			}
			if errors.Is(err, ErrInProgress) {
				t.Fatalf("unknown durable state reported as in progress: %v", err)
			}
			if !reflect.DeepEqual(canceledRecord, Record{}) {
				t.Fatalf("canceled caller received fabricated record: %+v", canceledRecord)
			}

			store.mu.Lock()
			store.getError = nil
			store.mu.Unlock()
			if !test.claimantFinishes {
				stored := store.snapshot(t, request.IdempotenceKey)
				if stored.State != ExecutionRunning || stored.Outcome != OutcomeUnknown || stored.Revision != 2 {
					t.Fatalf("canceled caller mutated durable state: %+v", stored)
				}
				close(effectRelease)
			}
			if err := <-bDone; err != nil {
				t.Fatalf("claimant error = %v", err)
			}
			if got := effectCalls.Load(); got != 1 {
				t.Fatalf("effect calls = %d", got)
			}
			audits := auditor.snapshots()
			if len(audits) != 1 || audits[0].Outcome != OutcomeAccepted || audits[0].State != ExecutionFinished {
				t.Fatalf("audit attempts = %+v", audits)
			}
			stored := store.snapshot(t, request.IdempotenceKey)
			if stored.Outcome != OutcomeAccepted || stored.State != ExecutionFinished || stored.Audit.State != AuditPersisted {
				t.Fatalf("final record = %+v", stored)
			}
		})
	}
}
