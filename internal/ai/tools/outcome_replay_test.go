package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/outcome"
)

// The outcome ledger replay and every effect-initiated record failure keep
// the same deterministic operation ID as the original execution.

func TestSendKeysOutcomeReplayKeepsOperationID(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	registry := NewRegistry(Dependencies{Terminal: terminal, Outcome: ledger, PollInterval: time.Millisecond})
	call := Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"true","enter":true}`), AuthorizationID: "guard:test"}
	scope := Scope{SessionID: "s", TabID: "t"}

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK || first.OperationID == "" {
		t.Fatalf("first execution = %+v", first)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if len(terminal.writes) != 1 {
		t.Fatalf("replayed effect ran %d times, want 1", len(terminal.writes))
	}
	if !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("second execution = %+v", second)
	}
	if second.OperationID != first.OperationID {
		t.Fatalf("replay lost the operation ID: %q vs %q", second.OperationID, first.OperationID)
	}
}

// failFinalizeStore lets the running-state write through and fails the
// finalize write after the effect ran.
type failFinalizeStore struct {
	inner   *outcome.SQLiteStore
	updates int
}

func (s *failFinalizeStore) Reserve(ctx context.Context, record outcome.Record) (outcome.Record, bool, error) {
	return s.inner.Reserve(ctx, record)
}

func (s *failFinalizeStore) Update(ctx context.Context, record outcome.Record, expectedRevision uint64) error {
	s.updates++
	if s.updates >= 2 {
		return errors.New("finalize write failed")
	}
	return s.inner.Update(ctx, record, expectedRevision)
}

func (s *failFinalizeStore) Get(ctx context.Context, key string) (outcome.Record, error) {
	return s.inner.Get(ctx, key)
}

func TestSendKeysRecordFailureKeepsOperationID(t *testing.T) {
	storage, _, sqliteStore := outcomeFixture(t)
	ledger, err := outcome.New(outcome.Options{Store: &failFinalizeStore{inner: sqliteStore}, Auditor: storage.OutcomeAuditor()})
	if err != nil {
		t.Fatal(err)
	}
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	registry := NewRegistry(Dependencies{Terminal: terminal, Outcome: ledger, PollInterval: time.Millisecond})
	call := Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"true","enter":true}`), AuthorizationID: "guard:test"}
	scope := Scope{SessionID: "s", TabID: "t"}

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK {
		t.Fatalf("record failure reported success: %+v", result)
	}
	if len(terminal.writes) != 1 {
		t.Fatalf("effect runs = %d, want 1", len(terminal.writes))
	}
	if !strings.Contains(result.Text, "操作已执行") {
		t.Fatalf("record failure contract = %q", result.Text)
	}
	if result.OperationID != operationID("job", "keys", "send_keys") {
		t.Fatalf("record failure lost the operation ID: %+v", result)
	}
}
