package execbatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestExecuteRejectsConfirmationWhenCurrentChallengeIsNil(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Request, *fakeResolver)
	}{
		{name: "replaced by nonproduction target", mutate: func(request *Request, _ *fakeResolver) {
			request.SessionIDs = []string{"staging"}
		}},
		{name: "all production labels removed", mutate: func(_ *Request, resolver *fakeResolver) {
			target := resolver.targets["prod"]
			target.Environment = ""
			resolver.targets["prod"] = target
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var execCalls atomic.Int32
			transport := newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				execCalls.Add(1)
				code := 0
				return base.ExecResult{ExitCode: &code}, nil
			})
			production := targetFor("prod", transport)
			production.Environment = "production"
			production.Hostname = "api.production.example"
			staging := targetFor("staging", successTransport())
			staging.Environment = "staging"
			resolver := &fakeResolver{targets: map[string]Target{"prod": production, "staging": staging}}
			service := New(Config{Resolver: resolver, Auditor: &confirmationAuditor{}})
			request := productionRequest()
			detail := requireProductionChallenge(t, service, request)
			test.mutate(&request, resolver)
			request.ProductionConfirmation = &ProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
			if _, err := service.Execute(t.Context(), "owner-a", request); !errors.Is(err, ErrStaleProductionConfirmation) {
				t.Fatalf("error = %v, want stale confirmation", err)
			}
			if got := execCalls.Load(); got != 0 {
				t.Fatalf("stale confirmation executed %d commands", got)
			}
		})
	}
}

func TestExecuteConfirmationEnvironmentFlipIsSerializedBeforeWorkers(t *testing.T) {
	var execCalls atomic.Int32
	var staging atomic.Bool
	resolveStarted := make(chan struct{})
	allowResolve := make(chan struct{})
	var calls atomic.Int32
	resolver := ResolverFunc(func(_ context.Context, sessionID string) (Target, error) {
		transport := newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			execCalls.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		})
		target := targetFor(sessionID, transport)
		if sessionID == "prod" && calls.Add(1) == 2 {
			close(resolveStarted)
			<-allowResolve
		}
		if sessionID == "prod" && !staging.Load() {
			target.Environment = "production"
			target.Hostname = "api.production.example"
		} else {
			target.Environment = "staging"
		}
		return target, nil
	})
	service := New(Config{Resolver: resolver, Auditor: &confirmationAuditor{}})
	request := productionRequest()
	detail := requireProductionChallenge(t, service, request)
	request.ProductionConfirmation = &ProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
	finished := make(chan error, 1)
	go func() {
		_, err := service.Execute(t.Context(), "owner-a", request)
		finished <- err
	}()
	<-resolveStarted
	staging.Store(true)
	close(allowResolve)
	if err := <-finished; !errors.Is(err, ErrStaleProductionConfirmation) {
		t.Fatalf("environment flip error = %v, want stale confirmation", err)
	}
	if got := execCalls.Load(); got != 0 {
		t.Fatalf("environment flip executed %d commands", got)
	}
}
