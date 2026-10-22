package forward

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A canceled recovery context surfaces as an error on every device: no rule
// is started, no session is reported, and no runtime is left behind.
func TestRecoverAutoStartRulesCanceledContextFailsEveryDevice(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	outcomes := fixture.service.RecoverAutoStartRules(ctx, []RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(connectCtx context.Context) (string, error) {
			if err := connectCtx.Err(); err != nil {
				return "", err
			}
			return fixture.connected.ID, nil
		},
	}})
	if len(outcomes) != 1 || !errors.Is(outcomes[0].Err, context.Canceled) {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[0].SessionID != "" || len(outcomes[0].Started) != 0 {
		t.Fatalf("canceled recovery reported progress: %+v", outcomes[0])
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("canceled recovery left runtimes: %+v", runtimes)
	}
	listed, err := fixture.service.RuleList(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != rule.ID || listed[0].Status != StatusStopped {
		t.Fatalf("canceled recovery changed the saved rule: %+v", listed)
	}
}

// A device still queued behind the concurrency semaphore when the context is
// canceled is never dialed: exactly the in-flight devices report their connect
// outcome and the queued one reports the cancellation without a session.
func TestRecoverAutoStartRulesCancelSkipsQueuedDevices(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	const devices = MaxConcurrentRecoveryConnections + 1
	release := make(chan struct{})
	dialed := make(chan string, devices)
	var dialCount atomic.Int32
	connectErr := errors.New("connect released after cancel")
	recoveryDevices := make([]RecoveryDevice, 0, devices)
	for range devices {
		asset, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{Kind: "ssh", Name: "queued-host", OptionsJSON: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		fixture.saveAutoStartRule(t, asset.ID, freeSavedRulePort(t), true)
		recoveryDevices = append(recoveryDevices, RecoveryDevice{
			AssetID: asset.ID,
			Connect: func(context.Context) (string, error) {
				dialCount.Add(1)
				dialed <- asset.ID
				<-release
				return "", connectErr
			},
		})
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	outcomesCh := make(chan []RecoveryOutcome, 1)
	go func() { outcomesCh <- fixture.service.RecoverAutoStartRules(ctx, recoveryDevices) }()
	for range MaxConcurrentRecoveryConnections {
		select {
		case <-dialed:
		case <-time.After(2 * time.Second):
			t.Fatal("recovery did not dial the first devices")
		}
	}
	// Let the queued device park behind the semaphore before canceling, then
	// keep the semaphore occupied briefly so its wait can only observe the
	// canceled context.
	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
	close(release)
	outcomes := <-outcomesCh
	if len(outcomes) != devices {
		t.Fatalf("outcomes = %d, want %d", len(outcomes), devices)
	}
	var canceled, released int
	for _, outcome := range outcomes {
		switch {
		case outcome.SessionID == "" && errors.Is(outcome.Err, context.Canceled):
			canceled++
		case errors.Is(outcome.Err, connectErr):
			released++
		default:
			t.Fatalf("unexpected outcome = %+v", outcome)
		}
	}
	if canceled != 1 || released != MaxConcurrentRecoveryConnections {
		t.Fatalf("canceled = %d, released = %d, outcomes = %+v", canceled, released, outcomes)
	}
	if got := int(dialCount.Load()); got != MaxConcurrentRecoveryConnections {
		t.Fatalf("dialed %d devices, want exactly the %d in-flight ones", got, MaxConcurrentRecoveryConnections)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("canceled recovery left runtimes: %+v", runtimes)
	}
}

// A canceled auto-start pass starts nothing and reports an error instead of
// silently succeeding with zero rules.
func TestStartAutoStartRulesCanceledContextStartsNothing(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	started, err := fixture.service.StartAutoStartRules(ctx, fixture.connected.ID)
	if err == nil {
		t.Fatal("canceled auto-start reported success")
	}
	if len(started) != 0 {
		t.Fatalf("canceled auto-start started rules: %+v", started)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("canceled auto-start left runtimes: %+v", runtimes)
	}
}
