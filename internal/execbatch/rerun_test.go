package execbatch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRerunEligibleStatuses(t *testing.T) {
	for _, test := range []struct {
		status Status
		want   bool
	}{
		{StatusSuccess, false},
		{StatusExitError, true},
		{StatusTransportError, true},
		{StatusTimeout, true},
		{StatusCanceled, false},
		{Status("weird"), false},
		{"", false},
	} {
		if got := RerunEligible(test.status); got != test.want {
			t.Fatalf("RerunEligible(%q) = %v, want %v", test.status, got, test.want)
		}
	}
}

func TestSelectRerunTargetsKeepsFailedOnlyInOrder(t *testing.T) {
	exitCode := 2
	results := []Result{
		{SessionID: "ok-1", Status: StatusSuccess},
		{SessionID: "exit", Status: StatusExitError, ExitCode: &exitCode},
		{SessionID: "ok-2", Status: StatusSuccess},
		{SessionID: "transport", Status: StatusTransportError},
		{SessionID: "canceled", Status: StatusCanceled},
		{SessionID: "timeout", Status: StatusTimeout},
	}
	selected := SelectRerunTargets(results)
	if strings.Join(selected, ",") != "exit,transport,timeout" {
		t.Fatalf("selected = %+v", selected)
	}
}

func TestSelectRerunTargetsEmptyWhenNothingFailed(t *testing.T) {
	if selected := SelectRerunTargets(nil); selected == nil || len(selected) != 0 {
		t.Fatalf("nil results selected = %#v, want empty", selected)
	}
	successes := []Result{
		{SessionID: "ok-1", Status: StatusSuccess},
		{SessionID: "ok-2", Status: StatusSuccess},
	}
	if selected := SelectRerunTargets(successes); len(selected) != 0 {
		t.Fatalf("all-success selected = %+v", selected)
	}
	canceled := []Result{
		{SessionID: "stop-1", Status: StatusCanceled},
		{SessionID: "stop-2", Status: StatusCanceled},
	}
	if selected := SelectRerunTargets(canceled); len(selected) != 0 {
		t.Fatalf("canceled-only selected = %+v", selected)
	}
}

func TestRerunRequestBuildsFailedOnlyRerun(t *testing.T) {
	exitCode := 2
	results := []Result{
		{SessionID: "ok-1", Status: StatusSuccess},
		{SessionID: "exit", Status: StatusExitError, ExitCode: &exitCode},
		{SessionID: "transport", Status: StatusTransportError},
		{SessionID: "canceled", Status: StatusCanceled},
		{SessionID: "timeout", Status: StatusTimeout},
	}
	original := Request{
		BatchID: "batch-1", Command: "deploy", TimeoutMS: 30000, MaxConcurrent: 4,
		ProductionConfirmation: &ProductionConfirmation{Confirmed: true},
		Runbook:                &RunbookMetadata{RunID: "run-1", StepNumber: 2, StepCount: 5},
	}
	request, err := RerunRequest(original, results, "batch-2")
	if err != nil {
		t.Fatal(err)
	}
	if request.BatchID != "batch-2" || request.Command != "deploy" || request.TimeoutMS != 30000 || request.MaxConcurrent != 4 {
		t.Fatalf("request = %+v", request)
	}
	if strings.Join(request.SessionIDs, ",") != "exit,transport,timeout" {
		t.Fatalf("sessions = %+v", request.SessionIDs)
	}
	if request.ProductionConfirmation != original.ProductionConfirmation {
		t.Fatal("rerun request dropped the caller's production confirmation")
	}
	if request.Runbook != nil {
		t.Fatal("rerun request kept the runbook metadata")
	}
}

func TestRerunRequestValidation(t *testing.T) {
	results := []Result{{SessionID: "exit", Status: StatusExitError}}
	original := Request{BatchID: "batch-1", Command: "deploy"}
	if _, err := RerunRequest(original, results, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty batch id = %v", err)
	}
	if _, err := RerunRequest(original, results, " lead"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("padded batch id = %v", err)
	}
	if _, err := RerunRequest(original, []Result{{SessionID: "ok", Status: StatusSuccess}}, "batch-2"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no failed targets = %v", err)
	}
}

type rerunCaptureExecutor struct {
	ownerID string
	request Request
	batch   Batch
	err     error
}

func (f *rerunCaptureExecutor) Execute(_ context.Context, ownerID string, request Request) (Batch, error) {
	f.ownerID = ownerID
	f.request = request
	return f.batch, f.err
}

func TestExecuteRerun(t *testing.T) {
	results := []Result{
		{SessionID: "ok", Status: StatusSuccess},
		{SessionID: "exit", Status: StatusExitError},
	}
	executor := &rerunCaptureExecutor{batch: Batch{BatchID: "rerun-done"}}
	finished, err := ExecuteRerun(context.Background(), executor, "owner-a", Request{Command: "deploy"}, results, "batch-2")
	if err != nil {
		t.Fatal(err)
	}
	if finished.BatchID != "rerun-done" || executor.ownerID != "owner-a" {
		t.Fatalf("finished = %+v, owner = %s", finished, executor.ownerID)
	}
	if executor.request.BatchID != "batch-2" || strings.Join(executor.request.SessionIDs, ",") != "exit" {
		t.Fatalf("request = %+v", executor.request)
	}

	failing := &rerunCaptureExecutor{err: errors.New("execute exploded")}
	if _, err := ExecuteRerun(context.Background(), failing, "owner-a", Request{Command: "deploy"}, results, "batch-3"); err == nil || err.Error() != "execute exploded" {
		t.Fatalf("executor error = %v", err)
	}
	if _, err := ExecuteRerun(context.Background(), nil, "owner-a", Request{Command: "deploy"}, results, "batch-4"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nil executor = %v", err)
	}
	if _, err := ExecuteRerun(context.Background(), executor, "owner-a", Request{Command: "deploy"}, []Result{{SessionID: "ok", Status: StatusSuccess}}, "batch-5"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no failed targets = %v", err)
	}
}
