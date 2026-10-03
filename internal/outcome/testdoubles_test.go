package outcome

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu           sync.Mutex
	records      map[string]Record
	reserveCalls int
	updateCalls  int
	afterReserve func(context.Context, Record)
	afterUpdate  func(context.Context, Record)
	updateError  func(Record) error
	getError     func(context.Context, string) error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{records: make(map[string]Record)}
}

func (s *memoryStore) Reserve(ctx context.Context, record Record) (Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	stored, exists := s.records[record.IdempotenceKey]
	reserved := !exists
	if reserved {
		stored = cloneRecord(record)
		s.records[record.IdempotenceKey] = cloneRecord(record)
	}
	s.reserveCalls++
	s.mu.Unlock()
	if s.afterReserve != nil {
		s.afterReserve(ctx, cloneRecord(stored))
	}
	return cloneRecord(stored), reserved, nil
}

func (s *memoryStore) Update(ctx context.Context, record Record, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	stored, exists := s.records[record.IdempotenceKey]
	if !exists {
		s.mu.Unlock()
		return ErrNotFound
	}
	if stored.Revision != expectedRevision {
		s.mu.Unlock()
		return ErrRevisionConflict
	}
	if record.Revision != expectedRevision+1 {
		s.mu.Unlock()
		return ErrRevisionConflict
	}
	if s.updateError != nil {
		if err := s.updateError(cloneRecord(record)); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.records[record.IdempotenceKey] = cloneRecord(record)
	s.updateCalls++
	s.mu.Unlock()
	if s.afterUpdate != nil {
		s.afterUpdate(ctx, cloneRecord(record))
	}
	return nil
}

func (s *memoryStore) Get(ctx context.Context, key string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	getError := s.getError
	s.mu.Unlock()
	if getError != nil {
		if err := getError(ctx, key); err != nil {
			return Record{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.records[key]
	if !exists {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (s *memoryStore) snapshot(t *testing.T, key string) Record {
	t.Helper()
	record, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func (s *memoryStore) counts() (reserves, updates int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserveCalls, s.updateCalls
}

type memoryAuditor struct {
	mu           sync.Mutex
	records      []Record
	err          error
	beforeAppend func(context.Context, Record)
}

func (a *memoryAuditor) Append(ctx context.Context, record Record) error {
	a.mu.Lock()
	a.records = append(a.records, cloneRecord(record))
	a.mu.Unlock()
	if a.beforeAppend != nil {
		a.beforeAppend(ctx, record)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.err
}

func (a *memoryAuditor) snapshots() []Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	records := make([]Record, len(a.records))
	for i := range a.records {
		records[i] = cloneRecord(a.records[i])
	}
	return records
}

func newTestLedger(t *testing.T, store Store, auditor Auditor) *Ledger {
	t.Helper()
	ledger, err := New(Options{Store: store, Auditor: auditor, AuditTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func testRequest(key string) Request {
	return Request{
		IdempotenceKey:  key,
		AuthorizationID: "authorization-1",
		Kind:            KindCommand,
		Arguments:       []byte(`{"command":"apply","options":{"force":true}}`),
	}
}

func intPointer(value int) *int {
	return &value
}
