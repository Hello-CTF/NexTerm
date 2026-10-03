package outcome

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExecuteBindsCanonicalRequestAndAudit(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("execute-accepted")
	request.Arguments = []byte(" { \"options\" : { \"force\" : true }, \"command\" : \"apply\" } ")

	effectCalls := 0
	record, err := ledger.Execute(context.Background(), request, func(ctx context.Context) (Completion, error) {
		effectCalls++
		if err := ctx.Err(); err != nil {
			t.Fatalf("effect context: %v", err)
		}
		running := store.snapshot(t, request.IdempotenceKey)
		if running.State != ExecutionRunning || running.Outcome != OutcomeUnknown {
			t.Fatalf("effect ran before durable running state: %+v", running)
		}
		return Completion{ExitCode: intPointer(0)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if effectCalls != 1 {
		t.Fatalf("effect calls = %d", effectCalls)
	}
	if string(record.CanonicalArguments) != `{"command":"apply","options":{"force":true}}` {
		t.Fatalf("canonical arguments = %s", record.CanonicalArguments)
	}
	if record.AuthorizationID != request.AuthorizationID || record.Kind != KindCommand {
		t.Fatalf("request binding missing: %+v", record)
	}
	if record.State != ExecutionFinished || record.Outcome != OutcomeAccepted {
		t.Fatalf("terminal execution = %+v", record)
	}
	if record.Result.ExitCode == nil || *record.Result.ExitCode != 0 || record.Result.Error != "" {
		t.Fatalf("exit result = %+v", record.Result)
	}
	if record.StartedAt == nil || record.FinishedAt == nil || record.CreatedAt.IsZero() {
		t.Fatalf("execution timestamps = %+v", record)
	}
	if record.Audit.State != AuditPersisted || record.Audit.CompletedAt == nil || record.Audit.Error != "" {
		t.Fatalf("audit outcome = %+v", record.Audit)
	}
	if record.Revision != 4 {
		t.Fatalf("revision = %d, want four persisted states", record.Revision)
	}
	if stored := store.snapshot(t, request.IdempotenceKey); !reflect.DeepEqual(stored, record) {
		t.Fatalf("returned and stored records differ:\nstored: %+v\nreturned: %+v", stored, record)
	}
	audits := auditor.snapshots()
	if len(audits) != 1 || audits[0].Outcome != OutcomeAccepted || audits[0].Audit.State != AuditPending {
		t.Fatalf("audit input = %+v", audits)
	}
}

func TestRejectedAndNotAttemptedOutcomes(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)

	tests := []struct {
		name    string
		outcome Outcome
		record  func(context.Context, Request, error) (Record, error)
	}{
		{name: "rejected", outcome: OutcomeRejected, record: ledger.Reject},
		{name: "not attempted", outcome: OutcomeNotAttempted, record: ledger.NotAttempted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason := errors.New(test.name)
			request := testRequest(test.name)
			record, err := test.record(context.Background(), request, reason)
			if !errors.Is(err, reason) {
				t.Fatalf("error = %v, want reason", err)
			}
			if record.State != ExecutionFinished || record.Outcome != test.outcome {
				t.Fatalf("record = %+v", record)
			}
			if record.StartedAt != nil || record.FinishedAt == nil || record.Result.Error != reason.Error() {
				t.Fatalf("attempt metadata = %+v", record)
			}
			if record.Audit.State != AuditPersisted {
				t.Fatalf("audit = %+v", record.Audit)
			}
		})
	}
}

func TestNonzeroExitIsDefiniteFailure(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("failed-exit")

	record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		return Completion{ExitCode: intPointer(23)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != OutcomeFailed || record.Result.ExitCode == nil || *record.Result.ExitCode != 23 {
		t.Fatalf("failed exit was not preserved: %+v", record)
	}
	if record.Audit.State != AuditPersisted {
		t.Fatalf("audit = %+v", record.Audit)
	}
}

func TestAmbiguousErrorIsUnknownAndDuplicateDoesNotRepeat(t *testing.T) {
	store := newMemoryStore()
	auditor := &memoryAuditor{}
	ledger := newTestLedger(t, store, auditor)
	request := testRequest("ambiguous")
	ambiguousErr := errors.New("connection lost after write")
	effectCalls := 0

	record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, ambiguousErr
	})
	if !errors.Is(err, ambiguousErr) {
		t.Fatalf("error = %v", err)
	}
	if record.Outcome != OutcomeUnknown || record.Result.Error != ambiguousErr.Error() {
		t.Fatalf("ambiguous record = %+v", record)
	}

	request.Arguments = []byte("{\n  \"options\": {\"force\": true}, \"command\": \"apply\"\n}")
	duplicate, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		effectCalls++
		return Completion{}, nil
	})
	if err != nil {
		t.Fatalf("duplicate returned error: %v", err)
	}
	if !reflect.DeepEqual(duplicate, record) {
		t.Fatalf("duplicate did not return original record:\nfirst: %+v\nnext: %+v", record, duplicate)
	}
	if effectCalls != 1 || len(auditor.snapshots()) != 1 {
		t.Fatalf("duplicate repeated work: effects=%d audits=%d", effectCalls, len(auditor.snapshots()))
	}
}

func TestInvalidCompletionBecomesUnknown(t *testing.T) {
	tests := []struct {
		name       string
		completion Completion
		err        error
	}{
		{name: "accepted with error", completion: Completion{Outcome: OutcomeAccepted}, err: errors.New("uncertain")},
		{name: "failed without evidence", completion: Completion{Outcome: OutcomeFailed}},
		{name: "rejected after attempt", completion: Completion{Outcome: OutcomeRejected}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			ledger := newTestLedger(t, store, &memoryAuditor{})
			record, err := ledger.Execute(context.Background(), testRequest(test.name), func(context.Context) (Completion, error) {
				return test.completion, test.err
			})
			if !errors.Is(err, ErrInvalidCompletion) {
				t.Fatalf("error = %v", err)
			}
			if record.Outcome != OutcomeUnknown || !strings.Contains(record.Result.Error, ErrInvalidCompletion.Error()) {
				t.Fatalf("invalid completion was not conservative: %+v", record)
			}
		})
	}
}

func TestIdempotenceConflictChecksRequestBinding(t *testing.T) {
	store := newMemoryStore()
	ledger := newTestLedger(t, store, &memoryAuditor{})
	request := testRequest("conflict")
	if _, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
		return Completion{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "arguments", mutate: func(r *Request) { r.Arguments = []byte(`{"command":"other"}`) }},
		{name: "authorization", mutate: func(r *Request) { r.AuthorizationID = "authorization-2" }},
		{name: "kind", mutate: func(r *Request) { r.Kind = KindFile }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conflicting := request
			test.mutate(&conflicting)
			effectCalled := false
			_, err := ledger.Execute(context.Background(), conflicting, func(context.Context) (Completion, error) {
				effectCalled = true
				return Completion{}, nil
			})
			if !errors.Is(err, ErrIdempotenceConflict) {
				t.Fatalf("error = %v", err)
			}
			if effectCalled {
				t.Fatal("conflicting request invoked effect")
			}
		})
	}
}

func TestCanonicalArgumentsRejectAmbiguousJSON(t *testing.T) {
	canonical, err := CanonicalizeArguments([]byte(`{"z":null,"a":[1.00,{"c":3,"b":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"a":[1.00,{"b":2,"c":3}],"z":null}` {
		t.Fatalf("canonical = %s", canonical)
	}

	invalid := []string{
		``,
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`{} {}`,
		`{"a":}`,
	}
	for _, input := range invalid {
		if _, err := CanonicalizeArguments([]byte(input)); err == nil {
			t.Errorf("CanonicalizeArguments(%q) succeeded", input)
		}
	}
}

func TestCanonicalArgumentsPreserveLargeNumberLiterals(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "top level", input: `1e1000`, want: `1e1000`},
		{name: "object", input: `{"n":1e1000}`, want: `{"n":1e1000}`},
		{name: "array", input: `[1e1000]`, want: `[1e1000]`},
		{
			name:  "nested object and array",
			input: ` { "z" : [1e1000, { "n" : -1E+1000 }], "a" : 1.00 } `,
			want:  `{"a":1.00,"z":[1e1000,{"n":-1E+1000}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := CanonicalizeArguments([]byte(test.input))
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != test.want {
				t.Fatalf("canonical = %s, want %s", canonical, test.want)
			}
		})
	}

	for _, input := range []string{`{"n":1e1000,"n":0}`, `[{"n":1e1000,"n":0}]`} {
		if _, err := CanonicalizeArguments([]byte(input)); err == nil {
			t.Errorf("CanonicalizeArguments(%q) accepted duplicate keys", input)
		}
	}
}
