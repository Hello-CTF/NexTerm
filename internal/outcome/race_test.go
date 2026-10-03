package outcome

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

// firstClaimGateStore parks the first running-state update so a second
// claimant can pass the pending check, win the claim, and leave the parked
// caller to lose the compare-and-swap.
type firstClaimGateStore struct {
	*memoryStore
	arrived chan struct{}
	release chan struct{}
	gated   atomic.Bool
}

func (s *firstClaimGateStore) Update(ctx context.Context, record Record, expectedRevision uint64) error {
	if record.State == ExecutionRunning && s.gated.CompareAndSwap(false, true) {
		s.arrived <- struct{}{}
		<-s.release
	}
	return s.memoryStore.Update(ctx, record, expectedRevision)
}

// TestClaimConflictReloadFailureReturnsNoFabricatedRecord covers a lost
// running-state compare-and-swap whose reload fails: the durable state is
// unknown, so the losing caller must receive no record rather than the
// unpersisted running proposal.
func TestClaimConflictReloadFailureReturnsNoFabricatedRecord(t *testing.T) {
	base := newMemoryStore()
	store := &firstClaimGateStore{
		memoryStore: base,
		arrived:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("claim-reload-failure")
	pending, err := ledger.newRecord(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, reserved, err := base.Reserve(context.Background(), pending); err != nil || !reserved {
		t.Fatalf("seed pending record: reserved=%v err=%v", reserved, err)
	}

	runningPersisted := make(chan struct{})
	base.afterUpdate = func(_ context.Context, record Record) {
		if record.State == ExecutionRunning {
			close(runningPersisted)
		}
	}
	effectRelease := make(chan struct{})
	var effectCalls atomic.Int64
	type executeResult struct {
		record Record
		err    error
	}
	loserDone := make(chan executeResult, 1)
	go func() {
		record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
			effectCalls.Add(1)
			return Completion{}, nil
		})
		loserDone <- executeResult{record: record, err: err}
	}()
	<-store.arrived

	claimantDone := make(chan error, 1)
	go func() {
		_, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
			effectCalls.Add(1)
			<-effectRelease
			return Completion{}, nil
		})
		claimantDone <- err
	}()
	<-runningPersisted

	getErr := errors.New("outcome store get failed")
	base.mu.Lock()
	base.getError = func(context.Context, string) error { return getErr }
	base.mu.Unlock()
	close(store.release)

	loser := <-loserDone
	if !errors.Is(loser.err, ErrRevisionConflict) || !errors.Is(loser.err, getErr) {
		t.Fatalf("losing claimant error = %v", loser.err)
	}
	if errors.Is(loser.err, ErrInProgress) {
		t.Fatalf("unknown durable state reported as in progress: %v", loser.err)
	}
	if !reflect.DeepEqual(loser.record, Record{}) {
		t.Fatalf("losing claimant received fabricated record: %+v", loser.record)
	}

	base.mu.Lock()
	base.getError = nil
	base.mu.Unlock()
	close(effectRelease)
	if err := <-claimantDone; err != nil {
		t.Fatalf("claimant error = %v", err)
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
