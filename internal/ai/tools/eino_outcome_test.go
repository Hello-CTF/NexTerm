package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// outcomeFixture opens a real migrated in-memory database and composes the
// production-shaped ledger on top of it: SQLiteStore over the application
// handle plus the store's audit-log auditor.
func outcomeFixture(t *testing.T) (*store.Store, *outcome.Ledger, *outcome.SQLiteStore) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	sqliteStore, err := outcome.NewSQLiteStore(storage.DB())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := outcome.New(outcome.Options{Store: sqliteStore, Auditor: storage.OutcomeAuditor()})
	if err != nil {
		t.Fatal(err)
	}
	return storage, ledger, sqliteStore
}

func outcomeExecution(ledger *outcome.Ledger, transport *fakeTransport) *Execution {
	deps := Dependencies{
		Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
	}
	if ledger != nil {
		deps.Outcome = ledger
	}
	return &Execution{
		JobID:      "job-1",
		Registry:   NewRegistry(deps),
		Scope:      Scope{SessionID: "session-1"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
}

func execCall(id, command string) Call {
	args, err := json.Marshal(ExecCommandsArgs{Commands: []string{command}})
	if err != nil {
		panic(err)
	}
	return Call{ID: id, Name: "exec_commands", Args: args}
}

func outcomeAuditRows(t *testing.T, storage *store.Store) []store.AuditRow {
	t.Helper()
	kind := store.OutcomeAuditKind
	rows, err := storage.AuditQuery(context.Background(), store.AuditQuery{Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func recordedOutcome(t *testing.T, sqliteStore *outcome.SQLiteStore, key string) outcome.Record {
	t.Helper()
	record, err := sqliteStore.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("outcome record %q: %v", key, err)
	}
	return record
}

// TestOutcomeDispatcherRecordsBoundSideEffectOnce drives the dispatcher's
// allow path end to end: the guard decision authorizes the call, the call is
// bound to that exact decision, and the ledger records the effect exactly
// once with a terminal audit row.
func TestOutcomeDispatcherRecordsBoundSideEffectOnce(t *testing.T) {
	storage, ledger, sqliteStore := outcomeFixture(t)
	transport := &fakeTransport{result: base.ExecResult{Stdout: "hi"}}
	execution := outcomeExecution(ledger, transport)
	call := execCall("call-1", "echo hi")

	result, err := execution.initial(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := transport.calls(); got != 1 {
		t.Fatalf("effect runs = %d, want 1", got)
	}

	record := recordedOutcome(t, sqliteStore, OutcomeKey("job-1", "call-1"))
	if record.Outcome != outcome.OutcomeAccepted || record.State != outcome.ExecutionFinished {
		t.Fatalf("record = %+v", record)
	}
	if record.Kind != outcome.KindCommand {
		t.Fatalf("record kind = %q", record.Kind)
	}
	if record.Result.ExitCode == nil || *record.Result.ExitCode != 0 {
		t.Fatalf("record exit code = %v", record.Result.ExitCode)
	}
	if record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("record audit = %+v", record.Audit)
	}

	// The binding is the hash of the actual guard decision for this call, not
	// a placeholder: recompute it from the same classification inputs.
	ruling := guard.ClassifyTool(call.Name, call.Args, execution.Permission)
	decision := guard.Decide(execution.Permission, ruling, guard.NewMemory())
	if decision.Action != guard.ActionAllow {
		t.Fatalf("test setup: decision = %+v", decision)
	}
	if want := GuardAuthorizationID(decision); record.AuthorizationID != want {
		t.Fatalf("authorization id = %q, want %q", record.AuthorizationID, want)
	}

	canonical, err := outcome.CanonicalizeArguments(call.Args)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.CanonicalArguments) != string(canonical) {
		t.Fatalf("canonical arguments = %s, want %s", record.CanonicalArguments, canonical)
	}

	audits := outcomeAuditRows(t, storage)
	if len(audits) != 1 {
		t.Fatalf("outcome audit rows = %d, want 1", len(audits))
	}
	if !strings.Contains(audits[0].PayloadJSON, record.AuthorizationID) {
		t.Fatalf("outcome audit payload missing the authorization binding: %s", audits[0].PayloadJSON)
	}
}

// TestOutcomeDispatcherReplayDoesNotDuplicate proves a redispatch of the same
// job/call identity replays the durable record: the effect does not run a
// second time and no second audit row is appended.
func TestOutcomeDispatcherReplayDoesNotDuplicate(t *testing.T) {
	storage, ledger, sqliteStore := outcomeFixture(t)
	transport := &fakeTransport{result: base.ExecResult{Stdout: "hi"}}
	execution := outcomeExecution(ledger, transport)
	call := execCall("call-1", "echo hi")

	first, err := execution.initial(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK {
		t.Fatalf("first result = %+v", first)
	}
	second, err := execution.initial(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("replay result = %+v", second)
	}
	if got := transport.calls(); got != 1 {
		t.Fatalf("effect runs = %d, want 1", got)
	}
	if rows := outcomeAuditRows(t, storage); len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d, want 1", len(rows))
	}
	if _, err := sqliteStore.Get(context.Background(), OutcomeKey("job-1", "call-1")); err != nil {
		t.Fatal(err)
	}
}

// TestOutcomeDispatcherUnknownStaysHonest covers the ambiguous-effect contract:
// a transport failure maps to outcome=unknown, the caller keeps the branch's
// own output, and a redispatch replays the unknown record instead of retrying
// the effect or claiming a definite failure.
func TestOutcomeDispatcherUnknownStaysHonest(t *testing.T) {
	storage, ledger, sqliteStore := outcomeFixture(t)
	transport := &fakeTransport{execErr: errors.New("connection reset")}
	execution := outcomeExecution(ledger, transport)
	call := execCall("call-1", "echo hi")

	first, err := execution.initial(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK || first.ExitCode != 1 || !strings.Contains(first.Text, "[执行失败:") {
		t.Fatalf("first result = %+v", first)
	}
	record := recordedOutcome(t, sqliteStore, OutcomeKey("job-1", "call-1"))
	if record.Outcome != outcome.OutcomeUnknown {
		t.Fatalf("record outcome = %q, want unknown", record.Outcome)
	}
	if record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("record audit = %+v", record.Audit)
	}

	second, err := execution.initial(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if second.OK {
		t.Fatalf("unknown replay must not report success: %+v", second)
	}
	if !strings.Contains(second.Text, "outcome=unknown") || strings.Contains(second.Text, "outcome=failed") {
		t.Fatalf("replay must stay honest about the unknown outcome: %q", second.Text)
	}
	if got := transport.calls(); got != 1 {
		t.Fatalf("unknown outcome must not be retried: effect runs = %d, want 1", got)
	}
	if rows := outcomeAuditRows(t, storage); len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d, want 1", len(rows))
	}
}

// TestOutcomeDispatcherUnboundCallKeepsPriorBehavior proves the cooperative
// contract: without a guard-decision binding the ledger is never consulted,
// and a bound call against a registry without a ledger executes exactly as it
// did before the ledger existed.
func TestOutcomeDispatcherUnboundCallKeepsPriorBehavior(t *testing.T) {
	t.Run("ledger present but call unbound", func(t *testing.T) {
		storage, ledger, sqliteStore := outcomeFixture(t)
		transport := &fakeTransport{result: base.ExecResult{Stdout: "hi"}}
		registry := NewRegistry(Dependencies{
			Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
			Outcome:   ledger,
		})
		result := registry.Execute(context.Background(), "job-1", Scope{SessionID: "session-1"}, execCall("call-1", "echo hi"), nil)
		if !result.OK {
			t.Fatalf("result = %+v", result)
		}
		if got := transport.calls(); got != 1 {
			t.Fatalf("effect runs = %d, want 1", got)
		}
		if _, err := sqliteStore.Get(context.Background(), OutcomeKey("job-1", "call-1")); !errors.Is(err, outcome.ErrNotFound) {
			t.Fatalf("unbound call must not be recorded: %v", err)
		}
		if rows := outcomeAuditRows(t, storage); len(rows) != 0 {
			t.Fatalf("outcome audit rows = %d, want 0", len(rows))
		}
	})

	t.Run("bound call but no ledger", func(t *testing.T) {
		storage, _, sqliteStore := outcomeFixture(t)
		transport := &fakeTransport{result: base.ExecResult{Stdout: "hi"}}
		registry := NewRegistry(Dependencies{
			Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		})
		call := execCall("call-1", "echo hi")
		call.AuthorizationID = "guard:0000"
		result := registry.Execute(context.Background(), "job-1", Scope{SessionID: "session-1"}, call, nil)
		if !result.OK {
			t.Fatalf("result = %+v", result)
		}
		if got := transport.calls(); got != 1 {
			t.Fatalf("effect runs = %d, want 1", got)
		}
		if _, err := sqliteStore.Get(context.Background(), OutcomeKey("job-1", "call-1")); !errors.Is(err, outcome.ErrNotFound) {
			t.Fatalf("call without a ledger must not be recorded: %v", err)
		}
		if rows := outcomeAuditRows(t, storage); len(rows) != 0 {
			t.Fatalf("outcome audit rows = %d, want 0", len(rows))
		}
	})
}

// TestOutcomeDispatcherRejectsConflictingAuthorization proves the binding is
// load-bearing: a redispatch of the same call identity under a different
// guard decision is rejected as an idempotence conflict before the effect,
// never silently reusing the earlier authorization.
func TestOutcomeDispatcherRejectsConflictingAuthorization(t *testing.T) {
	storage, ledger, sqliteStore := outcomeFixture(t)
	transport := &fakeTransport{result: base.ExecResult{Stdout: "hi"}}
	registry := NewRegistry(Dependencies{
		Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		Outcome:   ledger,
	})
	first := execCall("call-1", "echo hi")
	first.AuthorizationID = "guard:aaaa"
	if result := registry.Execute(context.Background(), "job-1", Scope{SessionID: "session-1"}, first, nil); !result.OK {
		t.Fatalf("first result = %+v", result)
	}
	second := execCall("call-1", "echo hi")
	second.AuthorizationID = "guard:bbbb"
	result := registry.Execute(context.Background(), "job-1", Scope{SessionID: "session-1"}, second, nil)
	if result.OK || !strings.Contains(result.Text, "冲突") {
		t.Fatalf("conflicting redispatch must be rejected: %+v", result)
	}
	if got := transport.calls(); got != 1 {
		t.Fatalf("conflicting redispatch must not reach the effect: runs = %d, want 1", got)
	}
	record := recordedOutcome(t, sqliteStore, OutcomeKey("job-1", "call-1"))
	if record.AuthorizationID != "guard:aaaa" {
		t.Fatalf("record authorization = %q, want the first binding", record.AuthorizationID)
	}
	if rows := outcomeAuditRows(t, storage); len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d, want 1", len(rows))
	}
}
