package execbatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeTransport struct {
	generation uint64
	alive      atomic.Bool
	exec       func(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

func newFakeTransport(exec func(context.Context, string, base.ExecOptions) (base.ExecResult, error)) *fakeTransport {
	transport := &fakeTransport{generation: 1, exec: exec}
	transport.alive.Store(true)
	return transport
}

func (t *fakeTransport) Kind() string       { return "ssh" }
func (t *fakeTransport) Generation() uint64 { return t.generation }
func (t *fakeTransport) IsAlive() bool      { return t.alive.Load() }
func (t *fakeTransport) Close() error {
	t.alive.Store(false)
	return nil
}
func (t *fakeTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *fakeTransport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	return t.exec(ctx, command, options)
}

type fakeResolver struct {
	targets map[string]Target
	errors  map[string]error
	mu      sync.Mutex
	calls   []string
}

func (r *fakeResolver) Resolve(_ context.Context, sessionID string) (Target, error) {
	r.mu.Lock()
	r.calls = append(r.calls, sessionID)
	r.mu.Unlock()
	if err := r.errors[sessionID]; err != nil {
		return Target{}, err
	}
	return r.targets[sessionID], nil
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type fakeAuditor struct {
	mu      sync.Mutex
	records []store.AuditInput
}

func (a *fakeAuditor) AuditInsert(_ context.Context, input store.AuditInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, input)
	return nil
}

func successTransport() *fakeTransport {
	return newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
}

func targetFor(id string, transport base.Transport) Target {
	return Target{SessionID: id, SessionName: "name-" + id, AssetID: "asset-" + id, Transport: transport, Generation: transport.Generation()}
}

func TestExecutePreflightsEveryTargetBeforeAnyExec(t *testing.T) {
	var execCalls atomic.Int32
	newTransport := func() *fakeTransport {
		return newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			execCalls.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		})
	}
	resolver := &fakeResolver{
		targets: map[string]Target{
			"s1": targetFor("s1", newTransport()),
			"s3": targetFor("s3", newTransport()),
		},
		errors: map[string]error{"s2": ErrForbidden},
	}
	service := New(Config{Resolver: resolver})
	_, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "batch-preflight", SessionIDs: []string{"s1", "s2", "s3"}, Command: "uname"})
	var preflight *PreflightError
	if !errors.As(err, &preflight) || len(preflight.Failures) != 1 || preflight.Failures[0].SessionID != "s2" {
		t.Fatalf("preflight error = %+v", err)
	}
	if got := resolver.callCount(); got != 3 {
		t.Fatalf("resolved %d targets, want all 3", got)
	}
	if got := execCalls.Load(); got != 0 {
		t.Fatalf("executed %d commands after failed preflight", got)
	}
}

func TestExecuteDefaultConcurrencyIsFourAndPreservesResultOrder(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan string, 9)
	release := make(chan struct{})
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"} {
		resolver.targets[id] = targetFor(id, newFakeTransport(func(_ context.Context, _ string, options base.ExecOptions) (base.ExecResult, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			if options.Timeout != DefaultTimeout || options.ExpectedGeneration != 1 || options.Limits.Stdout != DefaultOutputLimit {
				return base.ExecResult{}, errors.New("unexpected exec options")
			}
			started <- optionsID(options)
			<-release
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		}))
	}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	done := make(chan outcome, 1)
	ids := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"}
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "batch-concurrency", SessionIDs: ids, Command: "hostname"})
		done <- outcome{batch: batch, err: err}
	}()
	for range DefaultConcurrency {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("four default workers did not start")
		}
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got := maximum.Load(); got != DefaultConcurrency {
			t.Fatalf("maximum concurrency = %d, want %d", got, DefaultConcurrency)
		}
		for index, result := range result.batch.Results {
			if result.SessionID != ids[index] || result.Status != StatusSuccess {
				t.Fatalf("result %d = %+v", index, result)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batch did not finish")
	}
}

func optionsID(_ base.ExecOptions) string { return "started" }

func TestExecuteDistinguishesExitAndTransportFailures(t *testing.T) {
	exitCode := 17
	resolver := &fakeResolver{targets: map[string]Target{
		"exit": targetFor("exit", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			return base.ExecResult{ExitCode: &exitCode, Stderr: "failed"}, nil
		})),
		"transport": targetFor("transport", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			return base.ExecResult{}, errors.New("connection reset")
		})),
		"exit-error": targetFor("exit-error", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			return base.ExecResult{}, &base.ExitError{Code: 23, Signal: "TERM"}
		})),
	}}
	service := New(Config{Resolver: resolver})
	batch, err := service.Execute(t.Context(), "owner-a", Request{
		BatchID: "batch-status", SessionIDs: []string{"exit", "transport", "exit-error"}, Command: "check",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := batch.Results[0]; result.Status != StatusExitError || result.ExitCode == nil || *result.ExitCode != 17 || result.Error != "" {
		t.Fatalf("non-zero exit = %+v", result)
	}
	if result := batch.Results[1]; result.Status != StatusTransportError || result.ExitCode != nil || result.Error != "connection reset" {
		t.Fatalf("transport failure = %+v", result)
	}
	if result := batch.Results[2]; result.Status != StatusExitError || result.ExitCode == nil || *result.ExitCode != 23 || result.Signal != "TERM" {
		t.Fatalf("exit error = %+v", result)
	}
}

func TestExecuteTimeoutIsPerTarget(t *testing.T) {
	resolver := &fakeResolver{targets: map[string]Target{
		"slow": targetFor("slow", newFakeTransport(func(ctx context.Context, _ string, options base.ExecOptions) (base.ExecResult, error) {
			execCtx, cancel := context.WithTimeout(ctx, options.Timeout)
			defer cancel()
			<-execCtx.Done()
			return base.ExecResult{}, execCtx.Err()
		})),
	}}
	service := New(Config{Resolver: resolver})
	batch, err := service.Execute(t.Context(), "owner-a", Request{
		BatchID: "batch-timeout", SessionIDs: []string{"slow"}, Command: "sleep 1", TimeoutMS: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := batch.Results[0]; result.Status != StatusTimeout || result.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestExecuteCapsOutputAndAuditOmitsRawOutput(t *testing.T) {
	auditor := &fakeAuditor{}
	resolver := &fakeResolver{targets: map[string]Target{
		"s1": targetFor("s1", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			code := 0
			return base.ExecResult{Stdout: "raw-stdout-secret", Stderr: "raw-stderr-secret", ExitCode: &code}, nil
		})),
	}}
	service := New(Config{Resolver: resolver, Auditor: auditor, OutputLimit: 4})
	batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "batch-cap", SessionIDs: []string{"s1"}, Command: "show-secret"})
	if err != nil {
		t.Fatal(err)
	}
	result := batch.Results[0]
	if result.Stdout != "raw-" || result.Stderr != "raw-" || !result.Truncated {
		t.Fatalf("capped result = %+v", result)
	}
	if len(auditor.records) != 1 {
		t.Fatalf("audit records = %d", len(auditor.records))
	}
	record := auditor.records[0]
	if record.Source != "user" || record.Kind != "exec_batch" || record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("audit metadata = %+v", record)
	}
	encoded, err := json.Marshal(record.Payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "raw-stdout-secret") || strings.Contains(text, "raw-stderr-secret") || strings.Contains(text, "stdout") || strings.Contains(text, "stderr") {
		t.Fatalf("audit contains raw output: %s", text)
	}
}

func TestCancelIsOwnerBoundAndParallelWithExecute(t *testing.T) {
	contexts := map[string]chan context.Context{"a": make(chan context.Context, 1), "b": make(chan context.Context, 1)}
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for _, id := range []string{"a", "b"} {
		id := id
		resolver.targets[id] = targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			contexts[id] <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		}))
	}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	doneA := make(chan outcome, 1)
	doneB := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "batch-a", SessionIDs: []string{"a"}, Command: "wait"})
		doneA <- outcome{batch: batch, err: err}
	}()
	go func() {
		batch, err := service.Execute(t.Context(), "owner-b", Request{BatchID: "batch-b", SessionIDs: []string{"b"}, Command: "wait"})
		doneB <- outcome{batch: batch, err: err}
	}()
	ctxA := <-contexts["a"]
	ctxB := <-contexts["b"]
	if _, err := service.Cancel(t.Context(), "owner-b", "batch-a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner cancel = %v", err)
	}
	if ctxA.Err() != nil || ctxB.Err() != nil {
		t.Fatal("cross-owner cancel affected a batch")
	}
	if _, err := service.Cancel(t.Context(), "owner-b", "batch-b"); err != nil {
		t.Fatal(err)
	}
	if ctxB.Err() == nil || ctxA.Err() != nil {
		t.Fatal("owner cancel affected the wrong batch")
	}
	if result := <-doneB; result.err != nil || result.batch.Results[0].Status != StatusCanceled {
		t.Fatalf("canceled B = %+v, %v", result.batch, result.err)
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "batch-a"); err != nil {
		t.Fatal(err)
	}
	if result := <-doneA; result.err != nil || result.batch.Results[0].Status != StatusCanceled {
		t.Fatalf("canceled A = %+v, %v", result.batch, result.err)
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "batch-a"); err != nil {
		t.Fatalf("finished batch cancel should be accepted as an early cancel for any reuse of the id: %v", err)
	}
}

func TestCancelMarksQueuedTargetsWithoutStartingThem(t *testing.T) {
	started := make(chan context.Context, 1)
	var calls atomic.Int32
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for _, id := range []string{"s1", "s2", "s3"} {
		resolver.targets[id] = targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			calls.Add(1)
			started <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		}))
	}
	service := New(Config{Resolver: resolver})
	done := make(chan Batch, 1)
	go func() {
		batch, _ := service.Execute(t.Context(), "owner-a", Request{
			BatchID: "batch-queue", SessionIDs: []string{"s1", "s2", "s3"}, Command: "wait", MaxConcurrent: 1,
		})
		done <- batch
	}()
	<-started
	if _, err := service.Cancel(t.Context(), "owner-a", "batch-queue"); err != nil {
		t.Fatal(err)
	}
	batch := <-done
	if got := calls.Load(); got != 1 {
		t.Fatalf("exec calls after cancel = %d, want 1", got)
	}
	for _, result := range batch.Results {
		if result.Status != StatusCanceled {
			t.Fatalf("queued result = %+v", result)
		}
	}
}

func TestExecuteRejectsInvalidSingleCommandAndDuplicateTargets(t *testing.T) {
	service := New(Config{Resolver: &fakeResolver{}})
	for _, request := range []Request{
		{BatchID: "b", SessionIDs: []string{"s"}, Command: "printf one\nprintf two"},
		{BatchID: "b", SessionIDs: []string{"s", "s"}, Command: "hostname"},
		{BatchID: "b", SessionIDs: []string{"s"}, Command: "hostname", MaxConcurrent: MaxConcurrency + 1},
		{BatchID: "b", SessionIDs: []string{"s"}, Command: "hostname", TimeoutMS: MaxTimeout.Milliseconds() + 1},
	} {
		if _, err := service.Execute(t.Context(), "owner", request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("request %+v error = %v", request, err)
		}
	}
}
