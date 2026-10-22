package services

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

type fakeExecutor struct {
	requests []execbatch.Request
	batch    execbatch.Batch
	err      error
}

func (f *fakeExecutor) Execute(_ context.Context, _ string, request execbatch.Request) (execbatch.Batch, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return execbatch.Batch{}, f.err
	}
	batch := f.batch
	batch.BatchID = request.BatchID
	batch.Command = request.Command
	return batch, nil
}

func TestListUsesExecBatchAndKeepsPerHostFailuresVisible(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{batch: execbatch.Batch{Results: []execbatch.Result{
		{SessionID: "healthy", SessionName: "healthy", Status: execbatch.StatusSuccess, Stdout: "nginx.service loaded active running nginx\n"},
		{SessionID: "sudo", SessionName: "sudo", Status: execbatch.StatusExitError, Stderr: "sudo: a password is required"},
		{SessionID: "unsupported", SessionName: "unsupported", Status: execbatch.StatusExitError, Stderr: "sh: systemctl: command not found"},
	}}}
	result, err := New(executor).List(t.Context(), "owner", ListRequest{BatchID: "list", SessionIDs: []string{"healthy", "sudo", "unsupported"}, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 || executor.requests[0].Command != SystemdListCommand(true) {
		t.Fatalf("requests = %+v", executor.requests)
	}
	if len(result.Hosts) != 3 || len(result.Hosts[0].Units) != 1 || result.Hosts[1].FailureKind != "sudo" || result.Hosts[2].Systemd != "unsupported" {
		t.Fatalf("list result = %+v", result)
	}
}

func TestProductionConfirmationChallengePropagatesWithoutExecuting(t *testing.T) {
	t.Parallel()
	required := &execbatch.ConfirmationRequiredError{BatchID: "restart", TargetSetHash: "target", CommandHash: "command"}
	executor := &fakeExecutor{err: required}
	_, err := New(executor).Action(t.Context(), "owner", ActionRequest{BatchID: "restart", SessionIDs: []string{"prod"}, Unit: "nginx.service", Action: ActionRestart})
	if !errors.Is(err, execbatch.ErrConfirmationRequired) {
		t.Fatalf("production error = %v", err)
	}
	var challenge *execbatch.ConfirmationRequiredError
	if !errors.As(err, &challenge) || challenge.BatchID != "restart" {
		t.Fatalf("production challenge = %+v, %v", challenge, err)
	}
}

func TestNilExecutorIsExplicitlyUnsupported(t *testing.T) {
	t.Parallel()
	_, err := New(nil).Kill(t.Context(), "owner", KillRequest{BatchID: "kill", SessionID: "host", PID: 123})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nil executor error = %v", err)
	}
}
