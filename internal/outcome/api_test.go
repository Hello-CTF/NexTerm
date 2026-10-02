package outcome

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestNewRequiresStoreAndAuditor(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	if _, err := New(Options{Auditor: auditor}); err == nil {
		t.Fatal("New accepted a nil store")
	}
	if _, err := New(Options{Store: store}); err == nil {
		t.Fatal("New accepted a nil auditor")
	}
}

func TestGetReturnsCurrentSnapshot(t *testing.T) {
	store := newMemoryStore()
	ledger := newTestLedger(t, store, &memoryAuditor{})
	request := testRequest("get-record")
	executed, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		return Completion{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Get(context.Background(), request.IdempotenceKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record, executed) {
		t.Fatalf("Get = %+v, want %+v", record, executed)
	}
	if _, err := ledger.Get(context.Background(), ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty key error = %v", err)
	}
	if _, err := ledger.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestInvalidRequestsDoNotReserveOrExecute(t *testing.T) {
	store := newMemoryStore()
	ledger := newTestLedger(t, store, &memoryAuditor{})
	tests := []struct {
		name     string
		mutate   func(*Request)
		noEffect bool
	}{
		{name: "key", mutate: func(r *Request) { r.IdempotenceKey = " " }},
		{name: "authorization", mutate: func(r *Request) { r.AuthorizationID = "" }},
		{name: "kind", mutate: func(r *Request) { r.Kind = Kind("network") }},
		{name: "arguments", mutate: func(r *Request) { r.Arguments = []byte(`{"a":1,"a":2}`) }},
		{name: "effect", noEffect: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testRequest("invalid-" + test.name)
			if test.mutate != nil {
				test.mutate(&request)
			}
			effect := Effect(func(context.Context) (Completion, error) { return Completion{}, nil })
			if test.noEffect {
				effect = nil
			}
			if _, err := ledger.Execute(context.Background(), request, effect); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v", err)
			}
			reserves, updates := store.counts()
			if reserves != 0 || updates != 0 {
				t.Fatalf("invalid request reached store: reserves=%d updates=%d", reserves, updates)
			}
		})
	}
}
