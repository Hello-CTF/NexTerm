package execbatch

import (
	"context"
	"errors"
	"testing"
)

type admissionTestContextKey struct{}

func TestWithAdmissionNilPreservesContext(t *testing.T) {
	parent := context.WithValue(t.Context(), admissionTestContextKey{}, "parent")
	if got := WithAdmission(parent, nil); got != parent {
		t.Fatal("nil admission wrapped the parent context")
	}
	called := false
	if err := admit(parent, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("operation did not run without admission")
	}
}

func TestExecuteAdmissionDenialPreventsPreflight(t *testing.T) {
	denied := errors.New("account session revoked")
	resolver := &fakeResolver{targets: map[string]Target{
		"s1": targetFor("s1", successTransport()),
	}}
	service := New(Config{Resolver: resolver})
	admissions := 0
	ctx := WithAdmission(t.Context(), func(ctx context.Context, fn func() error) error {
		admissions++
		if got := ctx.Value(admissionTestContextKey{}); got != nil {
			t.Errorf("unexpected admission context value: %v", got)
		}
		return denied
	})
	_, err := service.Execute(ctx, "owner-a", Request{BatchID: "batch-denied", SessionIDs: []string{"s1"}, Command: "hostname"})
	if !errors.Is(err, denied) {
		t.Fatalf("Execute error = %v, want admission denial", err)
	}
	if admissions != 1 {
		t.Fatalf("admission calls = %d, want 1", admissions)
	}
	if got := resolver.callCount(); got != 0 {
		t.Fatalf("resolver calls after denial = %d, want 0", got)
	}
}

func TestExecuteAdmissionRunsOperationWithOriginalContext(t *testing.T) {
	parent := context.WithValue(t.Context(), admissionTestContextKey{}, "account-session")
	resolver := &fakeResolver{targets: map[string]Target{
		"s1": targetFor("s1", successTransport()),
	}}
	service := New(Config{Resolver: resolver})
	admissions := 0
	ctx := WithAdmission(parent, func(ctx context.Context, fn func() error) error {
		admissions++
		if got := ctx.Value(admissionTestContextKey{}); got != "account-session" {
			t.Errorf("admission context value = %v, want account-session", got)
		}
		return fn()
	})
	batch, err := service.Execute(ctx, "owner-a", Request{BatchID: "batch-admitted", SessionIDs: []string{"s1"}, Command: "hostname"})
	if err != nil {
		t.Fatal(err)
	}
	if admissions != 1 {
		t.Fatalf("admission calls = %d, want 1", admissions)
	}
	if len(batch.Results) != 1 || batch.Results[0].Status != StatusSuccess {
		t.Fatalf("admitted batch results = %+v", batch.Results)
	}
}
