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

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func openExecBatchHistoryStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func execBatchHistoryContext(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func TestExecuteHistoryReadbackClassifiesErrorsWithoutRawContent(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	resolver := &fakeResolver{targets: map[string]Target{
		"success": targetFor("success", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			code := 0
			return base.ExecResult{Stdout: "stdout-secret-never-persist", Stderr: "stderr-secret-never-persist", ExitCode: &code}, nil
		})),
		"exit": targetFor("exit", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			code := 17
			return base.ExecResult{Stderr: "exit-output-secret", ExitCode: &code}, nil
		})),
		"transport": targetFor("transport", newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			return base.ExecResult{}, errors.New("transport-error-secret-never-persist")
		})),
	}}
	service := New(Config{Resolver: resolver, History: database})
	batch, err := service.Execute(t.Context(), "owner-a", Request{
		BatchID: "history-readback", SessionIDs: []string{"success", "exit", "transport"},
		Command: "deploy --token=command-secret-never-persist",
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].Stdout == "" || batch.Results[2].Error == "" {
		t.Fatal("test fixture did not exercise live output and errors")
	}
	history, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), "history-readback")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Status != store.ExecBatchStatusFailed || history.Batch.TargetCount != 3 || history.Batch.FinishedAt == nil {
		t.Fatalf("history batch = %+v", history.Batch)
	}
	if len(history.Batch.CommandHash) != 64 || len(history.Batch.TargetSetHash) != 64 {
		t.Fatalf("history hashes = %+v", history.Batch)
	}
	if len(history.Results) != 3 || history.Results[0].Status != string(StatusSuccess) ||
		history.Results[1].Status != string(StatusExitError) || history.Results[1].ErrorKind != store.ExecBatchErrorRemoteExit ||
		history.Results[2].Status != string(StatusTransportError) || history.Results[2].ErrorKind != store.ExecBatchErrorTransport {
		t.Fatalf("history results = %+v", history.Results)
	}
	if history.Results[1].ExitCode == nil || *history.Results[1].ExitCode != 17 {
		t.Fatalf("history exit code = %+v", history.Results[1])
	}
	for _, result := range history.Results {
		if result.StartedAt == nil || result.FinishedAt == nil {
			t.Fatalf("missing target timestamps: %+v", result)
		}
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"command-secret-never-persist", "stdout-secret-never-persist", "stderr-secret-never-persist",
		"exit-output-secret", "transport-error-secret-never-persist",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("history leaked %q: %s", secret, encoded)
		}
	}
}

func TestExecuteHistoryRunningThenCanceledAndQueuedTargetTimes(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	started := make(chan struct{}, 1)
	transport := newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	resolver := &fakeResolver{targets: map[string]Target{
		"first": targetFor("first", transport), "queued": targetFor("queued", transport),
	}}
	service := New(Config{Resolver: resolver, History: database})
	type outcome struct {
		batch Batch
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{
			BatchID: "history-cancel", SessionIDs: []string{"first", "queued"}, Command: "wait", MaxConcurrent: 1,
		})
		done <- outcome{batch: batch, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("execution did not start")
	}
	running, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), "history-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if running.Batch.Status != store.ExecBatchStatusRunning || running.Batch.FinishedAt != nil || len(running.Results) != 0 {
		t.Fatalf("running history = %+v", running)
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "history-cancel"); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.batch.Results[0].Status != StatusCanceled || result.batch.Results[1].Status != StatusCanceled {
		t.Fatalf("live canceled results = %+v", result.batch.Results)
	}
	history, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), "history-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Status != store.ExecBatchStatusCanceled || len(history.Results) != 2 {
		t.Fatalf("canceled history = %+v", history)
	}
	for _, result := range history.Results {
		if result.Status != string(StatusCanceled) || result.ErrorKind != store.ExecBatchErrorCanceled || result.FinishedAt == nil {
			t.Fatalf("canceled target = %+v", result)
		}
	}
	if history.Results[0].StartedAt == nil || history.Results[1].StartedAt != nil {
		t.Fatalf("started/queued timestamps = %+v / %+v", history.Results[0], history.Results[1])
	}
}

type failingExecBatchHistory struct {
	err error
}

func (f failingExecBatchHistory) ExecBatchHistorySave(context.Context, store.ExecBatchRow, []store.ExecBatchResultRow) error {
	return f.err
}

func TestExecuteHistoryFailureIsObservableWithoutChangingBatch(t *testing.T) {
	var mu sync.Mutex
	var observed []error
	resolver := &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", successTransport())}}
	service := New(Config{
		Resolver: resolver,
		History:  failingExecBatchHistory{err: errors.New("injected history failure")},
		OnError: func(err error) {
			mu.Lock()
			observed = append(observed, err)
			mu.Unlock()
		},
	})
	batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "history-failure", SessionIDs: []string{"s1"}, Command: "hostname"})
	if err != nil || len(batch.Results) != 1 || batch.Results[0].Status != StatusSuccess {
		t.Fatalf("batch with failed history = %+v, %v", batch, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 2 || !strings.Contains(observed[0].Error(), "injected history failure") || !strings.Contains(observed[1].Error(), "injected history failure") {
		t.Fatalf("history errors = %+v", observed)
	}
}

func TestExecuteHistoryProductionMarkerAndRejectedConfirmation(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	var execCalls atomic.Int32
	auditor := &confirmationAuditor{}
	service := newConfirmationService(&execCalls, auditor)
	service.history = database
	request := productionRequest()
	detail := requireProductionChallenge(t, service, request)
	if _, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), request.BatchID); err == nil {
		t.Fatal("rejected production confirmation created history")
	}
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}
	if _, err := service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatal(err)
	}
	history, err := database.ExecBatchHistoryGet(execBatchHistoryContext("owner-a"), request.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if !history.Batch.Production || history.Batch.CommandHash != detail.CommandHash || history.Batch.TargetSetHash != detail.TargetSetHash {
		t.Fatalf("production history = %+v", history.Batch)
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "deploy --token") {
		t.Fatalf("production history contains command plaintext: %s", encoded)
	}
}

func TestExecuteHistoryKeepsConfiguredProductionMarkerAfterCancel(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	owner := execBatchHistoryContext("owner-a")
	if err := guard.SetProductionEnvironments(owner, database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	transport := newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return base.ExecResult{}, ctx.Err()
	})
	live := targetFor("live", transport)
	live.Environment = "live"
	service := New(Config{
		Resolver: &fakeResolver{targets: map[string]Target{"live": live}},
		Auditor:  &confirmationAuditor{}, History: database, ProductionSettings: database,
	})
	request := Request{BatchID: "batch-live-cancel", SessionIDs: []string{"live"}, Command: "deploy"}
	_, err := service.Execute(owner, "owner-a", request)
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) || !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("execute error = %v, want confirmation required", err)
	}
	detail := required.IPCDetail()
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "live" {
		t.Fatalf("required environment names = %v", detail.RequiredEnvironmentNames)
	}
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.Execute(owner, "owner-a", request)
		done <- err
	}()
	<-started
	running, err := database.ExecBatchHistoryGet(owner, request.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if !running.Batch.Production || running.Batch.Status != store.ExecBatchStatusRunning {
		t.Fatalf("running history = %+v", running.Batch)
	}
	if _, err := service.Cancel(owner, "owner-a", request.BatchID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("canceled execute = %v", err)
	}
	finished, err := database.ExecBatchHistoryGet(owner, request.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Batch.Production {
		t.Fatalf("finished history lost the production marker: %+v", finished.Batch)
	}
	if finished.Batch.Status != store.ExecBatchStatusCanceled {
		t.Fatalf("finished history status = %q", finished.Batch.Status)
	}
}
