package execbatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newManagedDeviceService(t *testing.T) (*Service, *atomic.Int32) {
	t.Helper()
	var execs atomic.Int32
	transport := newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execs.Add(1)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	target := targetFor("dev", transport)
	target.AssetID = "device/dev-1"
	target.Environment = guard.ManagedDeviceEnvironment
	// Settings are nil on purpose: the device rule must not depend on owner configuration.
	service := New(Config{
		Resolver: &fakeResolver{targets: map[string]Target{"dev": target}},
		Auditor:  &confirmationAuditor{},
	})
	return service, &execs
}

func TestManagedDeviceBatchRequiresConfirmationBeforeAnyExec(t *testing.T) {
	service, execs := newManagedDeviceService(t)
	request := Request{BatchID: "batch-device", SessionIDs: []string{"dev"}, Command: "uptime"}

	_, err := service.Execute(t.Context(), "owner-a", request)
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("execute error = %v, want confirmation required", err)
	}
	if got := required.RequiredEnvironmentNames; len(got) != 1 || got[0] != guard.ManagedDeviceEnvironment {
		t.Fatalf("required environment names = %v", got)
	}
	if execs.Load() != 0 {
		t.Fatal("device command ran without confirmation")
	}

	// A confirmation that does not type back the device environment name is rejected.
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: required.TargetSetHash, CommandHash: required.CommandHash,
		EnvironmentNames: []string{"staging"},
	}
	if _, err := service.Execute(t.Context(), "owner-a", request); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("wrong-name execute error = %v, want confirmation required", err)
	}
	if execs.Load() != 0 {
		t.Fatal("device command ran with a wrong confirmation")
	}

	request.ProductionConfirmation.EnvironmentNames = []string{guard.ManagedDeviceEnvironment}
	result, err := service.Execute(t.Context(), "owner-a", request)
	if err != nil {
		t.Fatalf("confirmed execute: %v", err)
	}
	if execs.Load() != 1 || len(result.Results) != 1 || result.Results[0].Status != StatusSuccess {
		t.Fatalf("execs=%d results=%+v", execs.Load(), result.Results)
	}
}

func TestManagedDevicePreflightNeverExecutes(t *testing.T) {
	service, execs := newManagedDeviceService(t)
	_, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "batch-device-2", SessionIDs: []string{"dev"}, Command: "rm -rf /tmp/x"})
	if !errors.Is(err, ErrConfirmationRequired) || execs.Load() != 0 {
		t.Fatalf("err=%v execs=%d", err, execs.Load())
	}
}
