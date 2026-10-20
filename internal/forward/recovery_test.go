package forward

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func (f *savedRuleFixture) saveAutoStartRule(t *testing.T, assetID string, listenPort int32, autoStart bool) Rule {
	t.Helper()
	rule, err := f.service.SaveRule(f.ctx, RuleInput{
		AssetID: assetID, Name: "auto-start", ListenPort: listenPort,
		TargetHost: "remote-only.invalid", TargetPort: 4567, AutoStart: autoStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func (f *savedRuleFixture) connectAsset(t *testing.T, assetID string) *session.Session {
	t.Helper()
	connected, err := f.manager.Connect(f.ctx, session.Asset{ID: assetID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return connected
}

func TestStartAutoStartRulesStartsOnlyAutoStartLoopbackRules(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	auto := fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	manual := fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), false)

	started, err := fixture.service.StartAutoStartRules(fixture.ctx, fixture.connected.ID)
	if err != nil {
		t.Fatalf("StartAutoStartRules: %v", err)
	}
	if len(started) != 1 || started[0].ID != auto.ID || started[0].Status != StatusActive {
		t.Fatalf("started rules = %+v", started)
	}
	if started[0].ListenHost != savedListenHost {
		t.Fatalf("auto-start listener bound to %q, want loopback", started[0].ListenHost)
	}
	listed := fixture.service.ListFor(fixture.ctx)
	if len(listed) != 1 || listed[0].ID != auto.ID {
		t.Fatalf("runtime list = %+v", listed)
	}
	assertEcho(t, dialForward(t, listed[0]), "auto-start")
	rules, err := fixture.service.RuleList(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Rule{}
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	if byID[auto.ID].Status != StatusActive {
		t.Fatalf("auto-start rule status = %q", byID[auto.ID].Status)
	}
	if byID[manual.ID].Status != StatusStopped {
		t.Fatalf("manual rule status = %q", byID[manual.ID].Status)
	}
}

func TestStartAutoStartRulesSkipsOtherAssetsAndRejectsUnknownSessions(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	otherAsset, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{Kind: "ssh", Name: "other-auto-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.saveAutoStartRule(t, otherAsset.ID, freeSavedRulePort(t), false)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)

	other := fixture.connectAsset(t, otherAsset.ID)
	started, err := fixture.service.StartAutoStartRules(fixture.ctx, other.ID)
	if err != nil {
		t.Fatalf("StartAutoStartRules: %v", err)
	}
	if len(started) != 0 {
		t.Fatalf("rules started for unrelated session: %+v", started)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("runtimes after unrelated session: %+v", runtimes)
	}
	if _, err := fixture.service.StartAutoStartRules(fixture.ctx, "missing-session"); err == nil {
		t.Fatal("unknown session accepted")
	}
}

func TestRecoverAutoStartRulesDefaultOffSkipsRulesWithoutFlag(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), false)
	var connects atomic.Int32
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(context.Context) (string, error) {
			connects.Add(1)
			return fixture.connected.ID, nil
		},
	}})
	if len(outcomes) != 1 || outcomes[0].Skipped == "" || outcomes[0].Err != nil {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if connects.Load() != 0 {
		t.Fatalf("default-off recovery dialed %d connections", connects.Load())
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("default-off recovery started runtimes: %+v", runtimes)
	}
}

func TestRecoverAutoStartRulesSkipsProductionAssets(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	production, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{
		Kind: "ssh", Name: "prod-host", OptionsJSON: "{}", Tags: "linux, Production",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.saveAutoStartRule(t, production.ID, freeSavedRulePort(t), true)
	var connects atomic.Int32
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{{
		AssetID: production.ID,
		Connect: func(context.Context) (string, error) {
			connects.Add(1)
			return "", nil
		},
	}})
	if len(outcomes) != 1 || outcomes[0].Skipped == "" || outcomes[0].Err != nil {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if connects.Load() != 0 {
		t.Fatal("production asset was dialed during recovery")
	}
}

func TestRecoverAutoStartRulesStartsRulesAndBindsLoopback(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(context.Context) (string, error) { return fixture.connected.ID, nil },
	}})
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	outcome := outcomes[0]
	if outcome.Err != nil || outcome.SessionID != fixture.connected.ID {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(outcome.Started) != 1 || outcome.Started[0] != rule.ID {
		t.Fatalf("started rules = %v", outcome.Started)
	}
	listed := fixture.service.ListFor(fixture.ctx)
	if len(listed) != 1 || listed[0].ListenHost != savedListenHost || listed[0].ListenPort != uint16(rule.ListenPort) {
		t.Fatalf("runtime list = %+v", listed)
	}
	assertEcho(t, dialForward(t, listed[0]), "recovered")
}

func TestRecoverAutoStartRulesCapsConcurrentConnections(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	const devices = 6
	type wired struct {
		assetID string
		session *session.Session
	}
	wiredDevices := make([]wired, 0, devices)
	recoveryDevices := make([]RecoveryDevice, 0, devices)
	var inFlight, maxInFlight atomic.Int32
	for range devices {
		asset, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{Kind: "ssh", Name: "bulk-host", OptionsJSON: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		fixture.saveAutoStartRule(t, asset.ID, freeSavedRulePort(t), true)
		connected := fixture.connectAsset(t, asset.ID)
		wiredDevices = append(wiredDevices, wired{assetID: asset.ID, session: connected})
	}
	for _, device := range wiredDevices {
		recoveryDevices = append(recoveryDevices, RecoveryDevice{
			AssetID: device.assetID,
			Connect: func(context.Context) (string, error) {
				current := inFlight.Add(1)
				for {
					peak := maxInFlight.Load()
					if current <= peak || maxInFlight.CompareAndSwap(peak, current) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				inFlight.Add(-1)
				return device.session.ID, nil
			},
		})
	}
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, recoveryDevices)
	if len(outcomes) != devices {
		t.Fatalf("outcomes = %d, want %d", len(outcomes), devices)
	}
	for _, outcome := range outcomes {
		if outcome.Err != nil || len(outcome.Started) != 1 {
			t.Fatalf("outcome = %+v", outcome)
		}
	}
	if peak := maxInFlight.Load(); peak > MaxConcurrentRecoveryConnections {
		t.Fatalf("concurrent recovery connections = %d, want at most %d", peak, MaxConcurrentRecoveryConnections)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != devices {
		t.Fatalf("active runtimes = %d, want %d", len(runtimes), devices)
	}
}

func TestRecoverAutoStartRulesReportsConnectFailures(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	dialErr := errors.New("dial failed")
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(context.Context) (string, error) { return "", dialErr },
	}})
	if len(outcomes) != 1 || !errors.Is(outcomes[0].Err, dialErr) || outcomes[0].SessionID != "" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestRecoverAutoStartRulesValidatesDevices(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{
		{AssetID: "  ", Connect: func(context.Context) (string, error) { return fixture.connected.ID, nil }},
		{AssetID: fixture.asset.ID},
		{AssetID: fixture.asset.ID, Connect: func(context.Context) (string, error) { return fixture.connected.ID, nil }},
		{AssetID: fixture.asset.ID, Connect: func(context.Context) (string, error) { return fixture.connected.ID, nil }},
	})
	if len(outcomes) != 4 {
		t.Fatalf("outcomes = %d, want 4", len(outcomes))
	}
	if outcomes[0].Err == nil || outcomes[0].AssetID != "" {
		t.Fatalf("blank asset outcome = %+v", outcomes[0])
	}
	if outcomes[1].Err == nil {
		t.Fatalf("nil connect outcome = %+v", outcomes[1])
	}
	if outcomes[2].Err != nil || len(outcomes[2].Started) != 1 {
		t.Fatalf("first duplicate outcome = %+v", outcomes[2])
	}
	if outcomes[3].Skipped == "" {
		t.Fatalf("duplicate device outcome = %+v", outcomes[3])
	}
}

func TestRecoverAutoStartRulesRejectsForeignSession(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	outcomes := fixture.service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(context.Context) (string, error) { return "not-a-session", nil },
	}})
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestSavedRuleAutoStartPersistsAcrossServiceRebuild(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	if err := fixture.service.Close(); err != nil {
		t.Fatal(err)
	}
	rebuilt := newSavedRuleService(t, fixture.db, fixture.manager)
	rules, err := rebuilt.RuleList(fixture.ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules after rebuild = %+v, %v", rules, err)
	}
	if !rules[0].AutoStart {
		t.Fatal("autoStart flag lost across service rebuild")
	}
	if rules[0].Status != StatusStopped {
		t.Fatalf("rebuilt rule status = %q", rules[0].Status)
	}
	started, err := rebuilt.StartAutoStartRules(fixture.ctx, fixture.connected.ID)
	if err != nil || len(started) != 1 || started[0].ID != rule.ID {
		t.Fatalf("auto-start after rebuild = %+v, %v", started, err)
	}
}

func TestRecoverAutoStartRulesUnavailableEnvironment(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	fixture.saveAutoStartRule(t, fixture.asset.ID, freeSavedRulePort(t), true)
	service := NewService(Config{
		Provider: fixture.manager,
		Rules:    fixture.db,
		Policy:   Policy{Platform: "lazycat"},
	})
	t.Cleanup(func() { _ = service.Close() })
	outcomes := service.RecoverAutoStartRules(fixture.ctx, []RecoveryDevice{{
		AssetID: fixture.asset.ID,
		Connect: func(context.Context) (string, error) { return fixture.connected.ID, nil },
	}})
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}
