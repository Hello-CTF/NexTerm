package execbatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type confirmationSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *confirmationSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *confirmationSettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

type confirmationEnvironmentFixture struct {
	service *Service
	execs   *atomic.Int32
	auditor *confirmationAuditor
}

func newConfirmationEnvironmentFixture(t *testing.T, settings guard.Settings) *confirmationEnvironmentFixture {
	t.Helper()
	var execs atomic.Int32
	newTransport := func() *fakeTransport {
		return newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			execs.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		})
	}
	production := targetFor("prod", newTransport())
	production.SessionName = "Production API"
	production.Hostname = "api.production.example"
	production.Environment = "production"
	live := targetFor("live", newTransport())
	live.SessionName = "Live API"
	live.Hostname = "api.live.example"
	live.Environment = "live"
	staging := targetFor("staging", newTransport())
	staging.SessionName = "Staging API"
	staging.Hostname = "api.staging.example"
	staging.Environment = "staging"
	auditor := &confirmationAuditor{}
	service := New(Config{
		Resolver:           &fakeResolver{targets: map[string]Target{"prod": production, "live": live, "staging": staging}},
		Auditor:            auditor,
		Now:                func() time.Time { return time.UnixMilli(123456789) },
		ProductionSettings: settings,
	})
	return &confirmationEnvironmentFixture{service: service, execs: &execs, auditor: auditor}
}

func (f *confirmationEnvironmentFixture) requireChallenge(t *testing.T, ownerID string, request Request) ConfirmationRequiredDetail {
	t.Helper()
	_, err := f.service.Execute(t.Context(), ownerID, request)
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) || !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("execute error = %v, want confirmation required", err)
	}
	return required.IPCDetail()
}

func (f *confirmationEnvironmentFixture) requireNoExec(t *testing.T, ownerID string, request Request, want error) {
	t.Helper()
	if _, err := f.service.Execute(t.Context(), ownerID, request); !errors.Is(err, want) {
		t.Fatalf("execute error = %v, want %v", err, want)
	}
	if got := f.execs.Load(); got != 0 {
		t.Fatalf("executed %d commands", got)
	}
	if got := len(f.auditor.snapshot()); got != 0 {
		t.Fatalf("recorded %d audits", got)
	}
}

func TestExecuteProductionRejectsWrongEnvironmentName(t *testing.T) {
	fixture := newConfirmationEnvironmentFixture(t, nil)
	request := productionRequest()
	detail := fixture.requireChallenge(t, "owner-a", request)
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("required environment names = %v", detail.RequiredEnvironmentNames)
	}
	for _, test := range []struct {
		name  string
		names []string
	}{
		{name: "wrong name", names: []string{"staging"}},
		{name: "builtin alias is not the actual name", names: []string{"prod"}},
		{name: "missing names", names: nil},
		{name: "empty name", names: []string{""}},
		{name: "extra name", names: []string{"production", "staging"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConfirmationEnvironmentFixture(t, nil)
			request := productionRequest()
			detail := fixture.requireChallenge(t, "owner-a", request)
			request.ProductionConfirmation = &ProductionConfirmation{
				Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
				EnvironmentNames: test.names,
			}
			fixture.requireNoExec(t, "owner-a", request, ErrConfirmationRequired)
		})
	}

	t.Run("order case and surrounding space are ignored", func(t *testing.T) {
		fixture := newConfirmationEnvironmentFixture(t, nil)
		request := productionRequest()
		detail := fixture.requireChallenge(t, "owner-a", request)
		request.ProductionConfirmation = &ProductionConfirmation{
			Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
			EnvironmentNames: []string{" Production "},
		}
		batch, err := fixture.service.Execute(t.Context(), "owner-a", request)
		if err != nil {
			t.Fatalf("valid typed name rejected: %v", err)
		}
		if len(batch.Results) != 2 || fixture.execs.Load() != 2 {
			t.Fatalf("confirmed batch = %+v, execs = %d", batch.Results, fixture.execs.Load())
		}
	})
}

func TestExecuteProductionRejectsCancelledConfirmation(t *testing.T) {
	fixture := newConfirmationEnvironmentFixture(t, nil)
	request := productionRequest()
	detail := fixture.requireChallenge(t, "owner-a", request)
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: false, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}
	fixture.requireNoExec(t, "owner-a", request, ErrConfirmationRequired)
}

func TestExecuteProductionConfirmationIsOwnerBound(t *testing.T) {
	fixture := newConfirmationEnvironmentFixture(t, nil)
	request := productionRequest()
	detailA := fixture.requireChallenge(t, "owner-a", request)

	// The same batch under another owner is a different challenge: the first
	// owner's confirmation and typed names never authorize the second owner.
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detailA.TargetSetHash, CommandHash: detailA.CommandHash,
		EnvironmentNames: detailA.RequiredEnvironmentNames,
	}
	fixture.requireNoExec(t, "owner-b", request, ErrConfirmationRequired)

	detailB := fixture.requireChallenge(t, "owner-b", request)
	if detailB.TargetSetHash == detailA.TargetSetHash {
		t.Fatal("target-set hash is not owner-bound")
	}
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detailB.TargetSetHash, CommandHash: detailB.CommandHash,
		EnvironmentNames: detailB.RequiredEnvironmentNames,
	}
	batch, err := fixture.service.Execute(t.Context(), "owner-b", request)
	if err != nil {
		t.Fatalf("owner-b confirmation failed: %v", err)
	}
	if len(batch.Results) != 2 || fixture.execs.Load() != 2 {
		t.Fatalf("owner-b batch = %+v, execs = %d", batch.Results, fixture.execs.Load())
	}
}

func ownerContext(ownerID string) context.Context {
	return ipc.WithUserID(context.Background(), ownerID)
}

func TestExecuteProductionChallengeUsesConfiguredEnvironmentNames(t *testing.T) {
	settings := &confirmationSettings{values: make(map[string]string)}
	if err := guard.SetProductionEnvironments(ownerContext("owner-a"), settings, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	fixture := newConfirmationEnvironmentFixture(t, settings)

	request := Request{BatchID: "batch-live", SessionIDs: []string{"live"}, Command: "deploy --live"}
	detail := fixture.requireChallenge(t, "owner-a", request)
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "live" {
		t.Fatalf("required environment names = %v", detail.RequiredEnvironmentNames)
	}
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: []string{"production"},
	}
	fixture.requireNoExec(t, "owner-a", request, ErrConfirmationRequired)
	request.ProductionConfirmation.EnvironmentNames = []string{"live"}
	if _, err := fixture.service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatalf("configured production confirmation failed: %v", err)
	}

	// Built-in names stay guarded for the same owner.
	prodRequest := Request{BatchID: "batch-prod", SessionIDs: []string{"prod"}, Command: "deploy --prod"}
	prodDetail := fixture.requireChallenge(t, "owner-a", prodRequest)
	if len(prodDetail.RequiredEnvironmentNames) != 1 || prodDetail.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("built-in required environment names = %v", prodDetail.RequiredEnvironmentNames)
	}

	// Another owner without the configuration executes the same live target directly.
	other := Request{BatchID: "batch-live-b", SessionIDs: []string{"live"}, Command: "deploy --live"}
	batch, err := fixture.service.Execute(t.Context(), "owner-b", other)
	if err != nil {
		t.Fatalf("unconfigured owner challenged: %v", err)
	}
	if len(batch.Results) != 1 || batch.Results[0].Status != StatusSuccess {
		t.Fatalf("unconfigured owner batch = %+v", batch.Results)
	}
}

func TestExecuteProductionClassificationIsConcurrentSafe(t *testing.T) {
	settings := &confirmationSettings{values: make(map[string]string)}
	if err := guard.SetProductionEnvironments(ownerContext("owner-a"), settings, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	fixture := newConfirmationEnvironmentFixture(t, settings)

	const rounds = 8
	var wait sync.WaitGroup
	errs := make(chan error, rounds*2)
	for round := range rounds {
		wait.Add(2)
		go func() {
			defer wait.Done()
			request := Request{BatchID: fmt.Sprintf("batch-live-a-%d", round), SessionIDs: []string{"live"}, Command: "deploy --live"}
			if _, err := fixture.service.Execute(ownerContext("owner-a"), "owner-a", request); !errors.Is(err, ErrConfirmationRequired) {
				errs <- fmt.Errorf("configured owner lost the production challenge: %w", err)
			}
		}()
		go func() {
			defer wait.Done()
			request := Request{BatchID: fmt.Sprintf("batch-live-b-%d", round), SessionIDs: []string{"live"}, Command: "deploy --live"}
			if _, err := fixture.service.Execute(ownerContext("owner-b"), "owner-b", request); err != nil {
				errs <- fmt.Errorf("unconfigured owner was challenged: %w", err)
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
