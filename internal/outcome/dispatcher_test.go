package outcome_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type fakeTransport struct {
	mu        sync.Mutex
	execCalls int
	execFunc  func(ctx context.Context, command string) (base.ExecResult, error)
	files     *fakeFS
}

func (f *fakeTransport) Kind() string       { return "fake" }
func (f *fakeTransport) Generation() uint64 { return 1 }
func (f *fakeTransport) Exec(ctx context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	f.mu.Lock()
	f.execCalls++
	f.mu.Unlock()
	return f.execFunc(ctx, command)
}
func (f *fakeTransport) Ping(context.Context) (time.Duration, error)         { return 0, nil }
func (f *fakeTransport) IsAlive() bool                                       { return true }
func (f *fakeTransport) Close() error                                        { return nil }
func (f *fakeTransport) FileSystem(context.Context) (base.FileSystem, error) { return f.files, nil }
func (f *fakeTransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execCalls
}

type fakeFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newFakeFS() *fakeFS { return &fakeFS{files: make(map[string][]byte)} }

func (f *fakeFS) List(context.Context, string) ([]base.FileEntry, error) { return nil, nil }
func (f *fakeFS) ReadFile(_ context.Context, name string, _ int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.files[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), content...), nil
}
func (f *fakeFS) WriteFile(_ context.Context, name string, content []byte, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = append([]byte(nil), content...)
	return nil
}
func (f *fakeFS) WriteFileVersion(_ context.Context, name string, content []byte, _ bool, expected conditional.Expectation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if expected.Exists {
		return base.ErrUnsupported
	}
	if _, exists := f.files[name]; exists {
		return &conditional.MismatchError{Expected: expected, Actual: conditional.Version{Exists: true}, Reason: "file already exists"}
	}
	f.files[name] = append([]byte(nil), content...)
	return nil
}
func (f *fakeFS) Mkdir(context.Context, string) error                      { return nil }
func (f *fakeFS) Rename(context.Context, string, string) error             { return nil }
func (f *fakeFS) Delete(context.Context, string, bool) error               { return nil }
func (f *fakeFS) Chmod(context.Context, string, fs.FileMode) error         { return nil }
func (f *fakeFS) Checksum(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeFS) Size(context.Context, string) (int64, error)              { return 0, nil }
func (f *fakeFS) OpenRead(context.Context, string) (base.RemoteReader, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeFS) OpenWrite(context.Context, string, bool) (base.RemoteWriter, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeFS) Exists(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[name]
	return ok, nil
}

type fakeTerminal struct {
	mu     sync.Mutex
	writes int
}

func (f *fakeTerminal) Snapshot(context.Context, string) (tools.Screen, error) {
	return tools.Screen{}, nil
}
func (f *fakeTerminal) Write(context.Context, string, []byte) error {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil
}

type fakeDatabase struct {
	mu        sync.Mutex
	calls     int
	queryFunc func(ctx context.Context, sql string) (db.QueryResult, error)
}

func (f *fakeDatabase) Tables(context.Context, string, string) ([]string, error) { return nil, nil }
func (f *fakeDatabase) Describe(context.Context, string, string, string) (db.TableDescribe, error) {
	return db.TableDescribe{}, nil
}
func (f *fakeDatabase) Query(ctx context.Context, _ string, sql string, _ uint64, _ time.Duration) (db.QueryResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.queryFunc(ctx, sql)
}
func (f *fakeDatabase) RedisScan(context.Context, string, uint64, string, uint64) (db.RedisScanResult, error) {
	return db.RedisScanResult{}, nil
}

type dispatcher struct {
	storage   *store.Store
	ledger    *outcome.Ledger
	registry  *tools.Registry
	transport *fakeTransport
	database  *fakeDatabase
	terminal  *fakeTerminal
	files     *fakeFS
}

// newDispatcher wires a tool registry to a real migrated SQLite database:
// the outcome ledger persists through the 0004 schema and its auditor writes
// into the same audit_log the audit query reads.
func newDispatcher(t *testing.T, auditor func(*store.Store) outcome.Auditor) *dispatcher {
	t.Helper()
	storage, sqliteStore := openMigratedStore(t)
	ledger, err := outcome.New(outcome.Options{Store: sqliteStore, Auditor: auditor(storage)})
	if err != nil {
		t.Fatal(err)
	}
	files := newFakeFS()
	d := &dispatcher{
		storage: storage,
		ledger:  ledger,
		transport: &fakeTransport{files: files, execFunc: func(context.Context, string) (base.ExecResult, error) {
			code := 0
			return base.ExecResult{Stdout: "ok", ExitCode: &code}, nil
		}},
		database: &fakeDatabase{queryFunc: func(context.Context, string) (db.QueryResult, error) {
			return db.QueryResult{Columns: []string{"n"}, Rows: [][]any{{1}}}, nil
		}},
		terminal: &fakeTerminal{},
		files:    files,
	}
	deps := tools.Dependencies{
		Transport: func(context.Context, string) (base.Transport, error) { return d.transport, nil },
		Terminal:  d.terminal,
		Database:  d.database,
		DockerExec: func(context.Context, string, string, string) (tools.ExecResult, error) {
			return tools.ExecResult{Output: "done", ExitCode: 0}, nil
		},
		DockerAct: func(context.Context, string, string, string) error { return nil },
		Outcome:   ledger,
	}
	deps = tools.WithStore(deps, storage, d.database)
	d.registry = tools.NewRegistry(deps)
	return d
}

func realAuditor(storage *store.Store) outcome.Auditor { return storage.OutcomeAuditor() }

func authorizationFor(t *testing.T, name string, args json.RawMessage, mode guard.Mode) string {
	t.Helper()
	config := guard.Config{Mode: mode}
	decision := guard.Decide(config, guard.ClassifyTool(name, args, config), nil)
	return tools.GuardAuthorizationID(decision)
}

func (d *dispatcher) record(t *testing.T, jobID, callID string) outcome.Record {
	t.Helper()
	record, err := d.ledger.Get(context.Background(), tools.OutcomeKey(jobID, callID))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func (d *dispatcher) outcomeAuditRows(t *testing.T) []store.AuditRow {
	t.Helper()
	kind := store.OutcomeAuditKind
	rows, err := d.storage.AuditQuery(context.Background(), store.AuditQuery{Kind: &kind, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func execCall(id, command string) tools.Call {
	return tools.Call{ID: id, Name: "exec_commands", Args: json.RawMessage(fmt.Sprintf(`{"commands":[%q]}`, command))}
}

func TestDispatcherExecRecordsAcceptedOutcome(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	output := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !output.OK || output.ExitCode != 0 {
		t.Fatalf("output = %+v", output)
	}
	if d.transport.calls() != 1 {
		t.Fatalf("exec calls = %d", d.transport.calls())
	}
	record := d.record(t, "job-1", "call-1")
	if record.Outcome != outcome.OutcomeAccepted || record.Kind != outcome.KindCommand || record.State != outcome.ExecutionFinished {
		t.Fatalf("record = %+v", record)
	}
	if record.AuthorizationID != call.AuthorizationID {
		t.Fatalf("authorization not bound: %+v", record)
	}
	if string(record.CanonicalArguments) != `{"commands":["apply"]}` {
		t.Fatalf("canonical arguments = %s", record.CanonicalArguments)
	}
	if record.Audit.State != outcome.AuditPersisted || record.Revision != 4 {
		t.Fatalf("record finalization = %+v", record)
	}

	rows := d.outcomeAuditRows(t)
	if len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d", len(rows))
	}
	if rows[0].Source != "ai" || rows[0].Kind != store.OutcomeAuditKind || rows[0].ExitCode == nil || *rows[0].ExitCode != 0 || rows[0].DurationMS == nil {
		t.Fatalf("outcome audit row = %+v", rows[0])
	}
	var audited outcome.Record
	if err := json.Unmarshal([]byte(rows[0].PayloadJSON), &audited); err != nil {
		t.Fatalf("audit payload: %v", err)
	}
	if audited.IdempotenceKey != tools.OutcomeKey("job-1", "call-1") || audited.Outcome != outcome.OutcomeAccepted {
		t.Fatalf("audit payload record = %+v", audited)
	}

	execKind := "exec"
	execRows, err := d.storage.AuditQuery(context.Background(), store.AuditQuery{Kind: &execKind, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(execRows) != 1 {
		t.Fatalf("call-level audit rows = %d", len(execRows))
	}
}

func TestDispatcherReplayDoesNotReexecute(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	first := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !first.OK {
		t.Fatalf("first output = %+v", first)
	}
	second := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !second.OK || !strings.Contains(second.Text, "已执行过") || !strings.Contains(second.Text, string(outcome.OutcomeAccepted)) {
		t.Fatalf("replay output = %+v", second)
	}
	if d.transport.calls() != 1 {
		t.Fatalf("replay re-ran the effect: calls = %d", d.transport.calls())
	}
	if rows := d.outcomeAuditRows(t); len(rows) != 1 {
		t.Fatalf("replay appended another audit row: %d", len(rows))
	}
	if record := d.record(t, "job-1", "call-1"); record.Revision != 4 {
		t.Fatalf("replay mutated the record: revision = %d", record.Revision)
	}
}

func TestDispatcherReplayAfterDefiniteFailureKeepsExitCode(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	d.transport.execFunc = func(context.Context, string) (base.ExecResult, error) {
		code := 23
		return base.ExecResult{Stdout: "denied", ExitCode: &code}, nil
	}
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	first := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !first.OK || first.ExitCode != 23 {
		t.Fatalf("first output = %+v", first)
	}
	if record := d.record(t, "job-1", "call-1"); record.Outcome != outcome.OutcomeFailed || record.Result.ExitCode == nil || *record.Result.ExitCode != 23 {
		t.Fatalf("record = %+v", record)
	}
	second := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !second.OK || second.ExitCode != 23 || !strings.Contains(second.Text, string(outcome.OutcomeFailed)) {
		t.Fatalf("replay output = %+v", second)
	}
	if d.transport.calls() != 1 {
		t.Fatalf("replay re-ran the effect: calls = %d", d.transport.calls())
	}
}

func TestDispatcherAmbiguousTransportErrorIsUnknownAndNeverRetried(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	d.transport.execFunc = func(context.Context, string) (base.ExecResult, error) {
		return base.ExecResult{}, errors.New("connection lost after write")
	}
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	output := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !output.OK || !strings.Contains(output.Text, "connection lost after write") {
		t.Fatalf("output = %+v", output)
	}
	record := d.record(t, "job-1", "call-1")
	if record.Outcome != outcome.OutcomeUnknown || !strings.Contains(record.Result.Error, "不明确") {
		t.Fatalf("record = %+v", record)
	}

	replay := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if replay.OK || !strings.Contains(replay.Text, string(outcome.OutcomeUnknown)) {
		t.Fatalf("replay output = %+v", replay)
	}
	if strings.Contains(replay.Text, string(outcome.OutcomeFailed)) {
		t.Fatalf("unknown outcome displayed as definite failure: %+v", replay)
	}
	if d.transport.calls() != 1 {
		t.Fatalf("unknown outcome was retried: calls = %d", d.transport.calls())
	}
}

func TestDispatcherCancellationRecordsUnknownAndFinalizes(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	ctx, cancel := context.WithCancel(context.Background())
	d.transport.execFunc = func(effectCtx context.Context, _ string) (base.ExecResult, error) {
		cancel()
		<-effectCtx.Done()
		return base.ExecResult{}, effectCtx.Err()
	}
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	output := d.registry.Execute(ctx, "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if output.OK || !strings.Contains(output.Text, context.Canceled.Error()) {
		t.Fatalf("output = %+v", output)
	}
	record := d.record(t, "job-1", "call-1")
	if record.Outcome != outcome.OutcomeUnknown || record.State != outcome.ExecutionFinished || record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("record = %+v", record)
	}
	replay := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if replay.OK || d.transport.calls() != 1 {
		t.Fatalf("canceled call was retried: calls = %d", d.transport.calls())
	}
}

func TestDispatcherAuditFailurePreservesOutcome(t *testing.T) {
	auditErr := errors.New("audit storage unavailable")
	auditor := &failingAuditor{err: auditErr}
	d := newDispatcher(t, func(*store.Store) outcome.Auditor { return auditor })
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	output := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !output.OK || !strings.Contains(output.Text, auditErr.Error()) {
		t.Fatalf("output = %+v", output)
	}
	record := d.record(t, "job-1", "call-1")
	if record.Outcome != outcome.OutcomeAccepted {
		t.Fatalf("audit failure changed the execution outcome: %+v", record)
	}
	if record.Audit.State != outcome.AuditFailed || record.Audit.Error != auditErr.Error() || record.Audit.CompletedAt == nil {
		t.Fatalf("audit failure not recorded: %+v", record.Audit)
	}
	if rows := d.outcomeAuditRows(t); len(rows) != 0 {
		t.Fatalf("failed audit wrote rows: %d", len(rows))
	}
	d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if auditor.attempts != 1 || d.transport.calls() != 1 {
		t.Fatalf("replay repeated work: audits=%d calls=%d", auditor.attempts, d.transport.calls())
	}
}

func TestDispatcherConflictingAuthorizationOrArgumentsNeverReachEffect(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)
	scope := tools.Scope{SessionID: "s"}

	if output := d.registry.Execute(context.Background(), "job-1", scope, call, nil); !output.OK {
		t.Fatalf("first output = %+v", output)
	}

	conflictingAuth := call
	conflictingAuth.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.Silent)
	if conflictingAuth.AuthorizationID == call.AuthorizationID {
		t.Fatal("test setup: expected different authorization ids for different decisions")
	}
	output := d.registry.Execute(context.Background(), "job-1", scope, conflictingAuth, nil)
	if output.OK || !strings.Contains(output.Text, "冲突") {
		t.Fatalf("conflicting authorization output = %+v", output)
	}

	conflictingArgs := call
	conflictingArgs.Args = json.RawMessage(`{"commands":["other"]}`)
	output = d.registry.Execute(context.Background(), "job-1", scope, conflictingArgs, nil)
	if output.OK || !strings.Contains(output.Text, "冲突") {
		t.Fatalf("conflicting arguments output = %+v", output)
	}

	if d.transport.calls() != 1 {
		t.Fatalf("conflicting requests reached the effect: calls = %d", d.transport.calls())
	}
	if record := d.record(t, "job-1", "call-1"); record.AuthorizationID != call.AuthorizationID || record.Outcome != outcome.OutcomeAccepted {
		t.Fatalf("stored record mutated: %+v", record)
	}
}

func TestDispatcherConcurrentSameCallHasSingleEffect(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	d.transport.execFunc = func(context.Context, string) (base.ExecResult, error) {
		time.Sleep(30 * time.Millisecond)
		code := 0
		return base.ExecResult{Stdout: "ok", ExitCode: &code}, nil
	}
	call := execCall("call-1", "apply")
	call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)

	const callers = 16
	outputs := make(chan tools.Output, callers)
	var done sync.WaitGroup
	done.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer done.Done()
			outputs <- d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
		}()
	}
	done.Wait()
	close(outputs)
	for output := range outputs {
		if !output.OK && !strings.Contains(output.Text, "正在执行中") {
			t.Errorf("unexpected concurrent output = %+v", output)
		}
	}
	if d.transport.calls() != 1 {
		t.Fatalf("effect calls = %d", d.transport.calls())
	}
	if rows := d.outcomeAuditRows(t); len(rows) != 1 {
		t.Fatalf("outcome audit rows = %d", len(rows))
	}
	if record := d.record(t, "job-1", "call-1"); record.Outcome != outcome.OutcomeAccepted || record.Audit.State != outcome.AuditPersisted {
		t.Fatalf("final record = %+v", record)
	}
}

func TestDispatcherUnboundCallExecutesUnrecorded(t *testing.T) {
	d := newDispatcher(t, realAuditor)
	call := execCall("call-1", "apply")

	output := d.registry.Execute(context.Background(), "job-1", tools.Scope{SessionID: "s"}, call, nil)
	if !output.OK {
		t.Fatalf("output = %+v", output)
	}
	if d.transport.calls() != 1 {
		t.Fatalf("unbound call did not execute: calls = %d", d.transport.calls())
	}
	if _, err := d.ledger.Get(context.Background(), tools.OutcomeKey("job-1", "call-1")); !errors.Is(err, outcome.ErrNotFound) {
		t.Fatalf("unbound call recorded an outcome: %v", err)
	}
	if rows := d.outcomeAuditRows(t); len(rows) != 0 {
		t.Fatalf("unbound call wrote outcome audit rows: %d", len(rows))
	}
}

func TestDispatcherSideEffectKinds(t *testing.T) {
	tests := []struct {
		tool     string
		args     string
		path     string
		scope    tools.Scope
		wantKind outcome.Kind
	}{
		{tool: "exec_commands", args: `{"commands":["apply"]}`, scope: tools.Scope{SessionID: "s"}, wantKind: outcome.KindCommand},
		{tool: "send_keys", args: `{"keys":"ls"}`, scope: tools.Scope{SessionID: "s", TabID: "t"}, wantKind: outcome.KindCommand},
		{tool: "docker_exec", args: `{"container_id":"web1","cmd":"ls"}`, scope: tools.Scope{SessionID: "s"}, wantKind: outcome.KindCommand},
		{tool: "docker_control", args: `{"container_id":"web1","action":"stop"}`, scope: tools.Scope{SessionID: "s"}, wantKind: outcome.KindCommand},
		{tool: "db_query", args: `{"sql":"SELECT 1"}`, scope: tools.Scope{ConnID: "c"}, wantKind: outcome.KindDatabase},
		{tool: "write_file", args: `{"path":"/new.txt","content":"x"}`, path: "/new.txt", scope: tools.Scope{SessionID: "s"}, wantKind: outcome.KindFile},
		{tool: "edit_file", args: `{"path":"/a.txt","old_string":"hello","new_string":"world"}`, path: "/a.txt", scope: tools.Scope{SessionID: "s"}, wantKind: outcome.KindFile},
	}
	for _, test := range tests {
		t.Run(test.tool, func(t *testing.T) {
			d := newDispatcher(t, realAuditor)
			if test.tool == "edit_file" {
				d.files.mu.Lock()
				d.files.files[test.path] = []byte("hello")
				d.files.mu.Unlock()
			}
			const jobID = "job-1"
			if test.path != "" {
				read := tools.Call{ID: "read-1", Name: "read_file", Args: json.RawMessage(fmt.Sprintf(`{"path":%q}`, test.path))}
				d.registry.Execute(context.Background(), jobID, test.scope, read, nil)
			}
			call := tools.Call{ID: "call-1", Name: test.tool, Args: json.RawMessage(test.args)}
			call.AuthorizationID = authorizationFor(t, call.Name, call.Args, guard.ReadWrite)
			var preparation *tools.Preparation
			if test.path != "" {
				prepared, err := d.registry.Prepare(context.Background(), jobID, test.scope, call)
				if err != nil {
					t.Fatal(err)
				}
				preparation = prepared
			}
			output := d.registry.Execute(context.Background(), jobID, test.scope, call, preparation)
			if !output.OK {
				t.Fatalf("output = %+v", output)
			}
			record := d.record(t, jobID, "call-1")
			if record.Kind != test.wantKind || record.Outcome != outcome.OutcomeAccepted {
				t.Fatalf("record = %+v, want kind %s", record, test.wantKind)
			}
		})
	}
}

type failingAuditor struct {
	mu       sync.Mutex
	attempts int
	err      error
}

func (f *failingAuditor) Append(context.Context, outcome.Record) error {
	f.mu.Lock()
	f.attempts++
	f.mu.Unlock()
	return f.err
}
