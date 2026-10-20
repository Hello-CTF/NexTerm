package cron

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

type fakeConnectEvidence struct {
	ownerMatch          bool
	ownerErr            error
	hostKey             guard.HostKeyState
	hostKeyErr          error
	vaultReady          bool
	vaultErr            error
	environment         string
	environmentErr      error
	productionConfirmed bool
	productionErr       error

	calls atomic.Int32
}

func (f *fakeConnectEvidence) OwnerMatch(context.Context, Trigger, string) (bool, error) {
	f.calls.Add(1)
	return f.ownerMatch, f.ownerErr
}

func (f *fakeConnectEvidence) HostKey(context.Context, Trigger, string) (guard.HostKeyState, error) {
	return f.hostKey, f.hostKeyErr
}

func (f *fakeConnectEvidence) VaultReady(context.Context, Trigger, string) (bool, error) {
	return f.vaultReady, f.vaultErr
}

func (f *fakeConnectEvidence) Environment(context.Context, Trigger, string) (string, error) {
	return f.environment, f.environmentErr
}

func (f *fakeConnectEvidence) ProductionConfirmed(context.Context, Trigger, string) (bool, error) {
	return f.productionConfirmed, f.productionErr
}

func allowAllEvidence() *fakeConnectEvidence {
	return &fakeConnectEvidence{
		ownerMatch: true, hostKey: guard.HostKeyKnown, vaultReady: true, environment: "staging",
	}
}

func allowAllConnector(dialCalls *atomic.Int32) *Connector {
	return &Connector{
		OptIn:    func(context.Context, Trigger) (bool, error) { return true, nil },
		Evidence: allowAllEvidence(),
		Dial: func(context.Context, Trigger, string) (string, error) {
			dialCalls.Add(1)
			return "session-connected", nil
		},
	}
}

func TestConnectorRequiresExplicitOptIn(t *testing.T) {
	var dials atomic.Int32
	connector := allowAllConnector(&dials)
	connector.OptIn = func(context.Context, Trigger) (bool, error) { return false, nil }
	if _, err := connector.Connect(context.Background(), testTrigger(), "asset-1"); !errors.Is(err, guard.ErrConnectOptInRequired) {
		t.Fatalf("connect without opt-in = %v", err)
	}
	connector.OptIn = nil
	if _, err := connector.Connect(context.Background(), testTrigger(), "asset-1"); !errors.Is(err, guard.ErrConnectOptInRequired) {
		t.Fatalf("connect with nil opt-in = %v", err)
	}
	if dials.Load() != 0 {
		t.Fatalf("dialed %d assets without opt-in", dials.Load())
	}
}

func TestConnectorEnforcesOwnerHostKeyVaultAndProduction(t *testing.T) {
	trigger := testTrigger()
	for _, test := range []struct {
		name   string
		mutate func(*Connector)
		want   error
	}{
		{name: "owner mismatch", mutate: func(c *Connector) {
			c.Evidence.(*fakeConnectEvidence).ownerMatch = false
		}, want: guard.ErrConnectOwnerMismatch},
		{name: "unknown host key is never auto-accepted", mutate: func(c *Connector) {
			c.Evidence.(*fakeConnectEvidence).hostKey = guard.HostKeyUnknown
		}, want: guard.ErrConnectHostKeyUnknown},
		{name: "changed host key", mutate: func(c *Connector) {
			c.Evidence.(*fakeConnectEvidence).hostKey = guard.HostKeyChanged
		}, want: guard.ErrConnectHostKeyChanged},
		{name: "vault unavailable", mutate: func(c *Connector) {
			c.Evidence.(*fakeConnectEvidence).vaultReady = false
		}, want: guard.ErrConnectVaultUnavailable},
		{name: "production needs confirmation", mutate: func(c *Connector) {
			c.Evidence.(*fakeConnectEvidence).environment = "production"
		}, want: guard.ErrConnectProductionConfirmationRequired},
		{name: "configured production needs confirmation", mutate: func(c *Connector) {
			c.Evidence.(*fakeConnectEvidence).environment = "live"
			c.Settings = productionListSettings(t, "live")
		}, want: guard.ErrConnectProductionConfirmationRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dials atomic.Int32
			connector := allowAllConnector(&dials)
			test.mutate(connector)
			if _, err := connector.Connect(context.Background(), trigger, "asset-1"); !errors.Is(err, test.want) {
				t.Fatalf("connect error = %v, want %v", err, test.want)
			}
			if dials.Load() != 0 {
				t.Fatalf("dialed %d assets after rejection", dials.Load())
			}
		})
	}
}

func TestConnectorConnectsWithEveryGateSatisfied(t *testing.T) {
	var dials atomic.Int32
	connector := allowAllConnector(&dials)
	sessionID, err := connector.Connect(context.Background(), testTrigger(), "asset-1")
	if err != nil || sessionID != "session-connected" {
		t.Fatalf("connect = %q, %v", sessionID, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("dial calls = %d", dials.Load())
	}

	connector.Evidence.(*fakeConnectEvidence).environment = "production"
	connector.Evidence.(*fakeConnectEvidence).productionConfirmed = true
	if _, err := connector.Connect(context.Background(), testTrigger(), "asset-1"); err != nil {
		t.Fatalf("confirmed production connect = %v", err)
	}
	if dials.Load() != 2 {
		t.Fatalf("dial calls after confirmed production = %d", dials.Load())
	}
}

func TestConnectorHonorsCancellation(t *testing.T) {
	var dials atomic.Int32
	connector := allowAllConnector(&dials)
	dialStarted := make(chan struct{})
	connector.Dial = func(ctx context.Context, _ Trigger, _ string) (string, error) {
		dials.Add(1)
		close(dialStarted)
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := connector.Connect(ctx, testTrigger(), "asset-1")
		done <- err
	}()
	<-dialStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled connect = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled connect did not return")
	}
	if dials.Load() != 1 {
		t.Fatalf("dial calls after cancellation = %d", dials.Load())
	}
}

func TestConnectorRejectsEmptyAsset(t *testing.T) {
	var dials atomic.Int32
	connector := allowAllConnector(&dials)
	if _, err := connector.Connect(context.Background(), testTrigger(), " "); !errors.Is(err, ErrConnectAssetRequired) {
		t.Fatalf("empty asset connect = %v", err)
	}
	if dials.Load() != 0 {
		t.Fatalf("dialed %d assets for an empty asset ID", dials.Load())
	}
}

func TestAgentExecutorConnectsDisconnectedAssetWithOptIn(t *testing.T) {
	var dials atomic.Int32
	scopeCalls := 0
	runner := &fakeAgentRunner{emit: func(stream agent.Stream) {
		_ = stream.Send(context.Background(), agent.Event{Type: "done"})
	}}
	executor := &AgentExecutor{
		Runner:  runner,
		Connect: allowAllConnector(&dials),
		ScopeFor: func(_ context.Context, trigger Trigger) (tools.Scope, error) {
			scopeCalls++
			if scopeCalls == 1 {
				return tools.Scope{}, &AssetDisconnectedError{AssetID: "asset-1"}
			}
			return tools.Scope{SessionID: "session-connected", AssetID: "asset-1"}, nil
		},
	}
	if err := executor.Execute(context.Background(), testTrigger()); err != nil {
		t.Fatalf("execute with opt-in connect = %v", err)
	}
	if dials.Load() != 1 || scopeCalls != 2 {
		t.Fatalf("dial calls = %d, scope calls = %d", dials.Load(), scopeCalls)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 1 || runner.args[0].Scope.SessionID != "session-connected" {
		t.Fatalf("started runs = %+v", runner.args)
	}
}

func TestAgentExecutorSkipsConnectWithoutOptIn(t *testing.T) {
	runner := &fakeAgentRunner{}
	executor := &AgentExecutor{
		Runner: runner,
		Connect: &Connector{
			OptIn:    func(context.Context, Trigger) (bool, error) { return false, nil },
			Evidence: allowAllEvidence(),
			Dial: func(context.Context, Trigger, string) (string, error) {
				t.Fatal("dialed without opt-in")
				return "", nil
			},
		},
		ScopeFor: func(context.Context, Trigger) (tools.Scope, error) {
			return tools.Scope{}, &AssetDisconnectedError{AssetID: "asset-1"}
		},
	}
	err := executor.Execute(context.Background(), testTrigger())
	if !errors.Is(err, guard.ErrConnectOptInRequired) {
		t.Fatalf("execute error = %v, want opt-in required", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 0 {
		t.Fatalf("started %d runs without opt-in", len(runner.args))
	}
}

func TestAgentExecutorFailsHonestlyWhenConnectRejected(t *testing.T) {
	var dials atomic.Int32
	connector := allowAllConnector(&dials)
	connector.Evidence.(*fakeConnectEvidence).hostKey = guard.HostKeyUnknown
	runner := &fakeAgentRunner{}
	executor := &AgentExecutor{
		Runner:  runner,
		Connect: connector,
		ScopeFor: func(context.Context, Trigger) (tools.Scope, error) {
			return tools.Scope{}, &AssetDisconnectedError{AssetID: "asset-1"}
		},
	}
	err := executor.Execute(context.Background(), testTrigger())
	if !errors.Is(err, guard.ErrConnectHostKeyUnknown) {
		t.Fatalf("execute error = %v, want host key rejection", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 0 {
		t.Fatalf("started %d runs after connect rejection", len(runner.args))
	}
}

func TestAgentExecutorDoesNotRetryScopeForever(t *testing.T) {
	var dials atomic.Int32
	scopeCalls := 0
	executor := &AgentExecutor{
		Runner:  &fakeAgentRunner{},
		Connect: allowAllConnector(&dials),
		ScopeFor: func(context.Context, Trigger) (tools.Scope, error) {
			scopeCalls++
			return tools.Scope{}, &AssetDisconnectedError{AssetID: "asset-1"}
		},
	}
	err := executor.Execute(context.Background(), testTrigger())
	if err == nil || !errors.Is(err, ErrAssetDisconnected) {
		t.Fatalf("execute error = %v, want the disconnected cause", err)
	}
	if dials.Load() != 1 || scopeCalls != 2 {
		t.Fatalf("dial calls = %d, scope calls = %d, want one connect and one retry", dials.Load(), scopeCalls)
	}
}

func TestAgentExecutorKeepsOrdinaryScopeFailuresOnTheOldPath(t *testing.T) {
	runner := &fakeAgentRunner{}
	executor := &AgentExecutor{
		Runner:  runner,
		Connect: allowAllConnector(&atomic.Int32{}),
		ScopeFor: func(context.Context, Trigger) (tools.Scope, error) {
			return tools.Scope{}, errors.New("conversation store offline")
		},
	}
	err := executor.Execute(context.Background(), testTrigger())
	if err == nil || !strings.Contains(err.Error(), "无法查询会话的终端范围") || !strings.Contains(err.Error(), "conversation store offline") {
		t.Fatalf("execute error = %v, want the honest scope-query failure", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 0 {
		t.Fatalf("started %d runs after a scope failure", len(runner.args))
	}
}

func productionListSettings(t *testing.T, names ...string) guard.Settings {
	t.Helper()
	settings := &memoryGuardSettings{values: make(map[string]string)}
	if err := guard.SetProductionEnvironments(context.Background(), settings, names); err != nil {
		t.Fatal(err)
	}
	return settings
}

type memoryGuardSettings struct {
	values map[string]string
}

func (s *memoryGuardSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *memoryGuardSettings) SettingSet(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}
