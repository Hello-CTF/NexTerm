package outcome

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentSameKeyHasSingleEffect(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("concurrent-key")
	const callers = 24

	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	var effectCalls atomic.Int64
	errorsCh := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
				effectCalls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return Completion{}, nil
			})
			if err != nil && !errors.Is(err, ErrInProgress) {
				errorsCh <- err
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent execute: %v", err)
	}
	if got := effectCalls.Load(); got != 1 {
		t.Fatalf("effect calls = %d", got)
	}
	if got := len(auditor.snapshots()); got != 1 {
		t.Fatalf("audit attempts = %d", got)
	}
	record := store.snapshot(t, request.IdempotenceKey)
	if record.Outcome != OutcomeAccepted || record.Audit.State != AuditPersisted {
		t.Fatalf("final record = %+v", record)
	}
}

func TestConcurrentIndependentKeys(t *testing.T) {
	store := newMemoryStore()
	ledger := newTestLedger(t, store, &memoryAuditor{})
	const callers = 16
	var done sync.WaitGroup
	done.Add(callers)
	errorsCh := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer done.Done()
			request := testRequest(fmt.Sprintf("independent-%d", index))
			record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
				return Completion{}, nil
			})
			if err != nil {
				errorsCh <- err
				return
			}
			if record.Outcome != OutcomeAccepted || record.Audit.State != AuditPersisted {
				errorsCh <- fmt.Errorf("record %d = %+v", index, record)
			}
		}(i)
	}
	done.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("independent execute: %v", err)
	}
}

type claimGateStore struct {
	*memoryStore
	arrived chan struct{}
	release chan struct{}
}

func (s *claimGateStore) Update(ctx context.Context, record Record, expectedRevision uint64) error {
	if record.State == ExecutionRunning {
		s.arrived <- struct{}{}
		<-s.release
	}
	return s.memoryStore.Update(ctx, record, expectedRevision)
}

func TestConcurrentPendingClaimUsesCompareAndSwap(t *testing.T) {
	base := newMemoryStore()
	store := &claimGateStore{
		memoryStore: base,
		arrived:     make(chan struct{}, 2),
		release:     make(chan struct{}),
	}
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("pending-claim-race")
	pending, err := ledger.newRecord(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, reserved, err := base.Reserve(context.Background(), pending); err != nil || !reserved {
		t.Fatalf("seed pending record: reserved=%v err=%v", reserved, err)
	}

	var effectCalls atomic.Int64
	var done sync.WaitGroup
	done.Add(2)
	errorsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer done.Done()
			_, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
				effectCalls.Add(1)
				return Completion{}, nil
			})
			if err != nil && !errors.Is(err, ErrInProgress) {
				errorsCh <- err
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-store.arrived:
		case <-time.After(2 * time.Second):
			close(store.release)
			done.Wait()
			t.Fatal("competing claims did not reach the compare-and-swap gate")
		}
	}
	close(store.release)
	done.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("competing claim: %v", err)
	}
	if got := effectCalls.Load(); got != 1 {
		t.Fatalf("effect calls = %d", got)
	}
	if got := len(auditor.snapshots()); got != 1 {
		t.Fatalf("audit attempts = %d", got)
	}
	if record := base.snapshot(t, request.IdempotenceKey); record.Outcome != OutcomeAccepted || record.Audit.State != AuditPersisted {
		t.Fatalf("final record = %+v", record)
	}
}
