//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A canceled connected-event pass logs the failure and leaves the auto-start
// rules dormant instead of starting a partial set.
func TestForwardRuntimeSessionConnectedCancelLogsAndStartsNothing(t *testing.T) {
	fixture := newForwardRuntimeFixture(t, startEchoListener(t))
	fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	connected, err := fixture.manager.Connect(fixture.ctx, session.Asset{ID: fixture.asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	handler := &captureSlogHandler{}
	fixture.runtime.logger = slog.New(handler)

	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	fixture.runtime.SessionConnected(ctx, connected.ID)

	found := false
	for _, message := range handler.messages {
		if message == "forward auto-start rules failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("canceled auto-start was not logged: %v", handler.messages)
	}
	if runtimes := fixture.forward.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("canceled auto-start left runtimes: %+v", runtimes)
	}
}

func forwardAutoStartDispatch(production *Production, command string, args json.RawMessage) ipc.Response {
	return production.Dispatcher.Dispatch(context.Background(), ipc.Request{
		Command: command, Args: args, ClientID: "autostart-e2e",
	}, production.Environment("autostart-e2e"))
}

// forwardRuleSettingsStatus reports the rule's status through the typed
// settings DTO, or "" when the rule or the data is not available yet.
func forwardRuleSettingsStatus(production *Production, ruleID string) string {
	response := forwardAutoStartDispatch(production, "forward_rule_settings_list", json.RawMessage(`{}`))
	if !response.OK {
		return ""
	}
	var settings []forwardRuleSettingsDTO
	if err := json.Unmarshal(response.Data, &settings); err != nil {
		return ""
	}
	for _, setting := range settings {
		if setting.ID == ruleID {
			return setting.Status
		}
	}
	return ""
}

func forwardRuntimeSpecByID(t *testing.T, production *Production, id string) forward.Spec {
	t.Helper()
	response := forwardAutoStartDispatch(production, "forward_list", json.RawMessage(`{}`))
	if !response.OK {
		t.Fatalf("forward_list dispatch = %+v", response.Error)
	}
	var specs []forward.Spec
	if err := json.Unmarshal(response.Data, &specs); err != nil {
		t.Fatalf("decode forward_list: %v", err)
	}
	for _, spec := range specs {
		if spec.ID == id {
			return spec
		}
	}
	t.Fatalf("runtime spec for %s not found in %+v", id, specs)
	return forward.Spec{}
}

func echoListenerPort(t *testing.T, upstream string) int32 {
	t.Helper()
	_, portText, err := net.SplitHostPort(upstream)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseInt(portText, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return int32(port)
}

func saveAutoStartRuleIPC(t *testing.T, production *Production, assetID, name string, listenPort, targetPort int32) forward.Rule {
	t.Helper()
	ctx := context.Background()
	saved := requireSavedRuleProductionResponse[forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ctx, "forward_rule_save", map[string]any{
			"assetId": assetID, "name": name, "listenPort": listenPort,
			"targetHost": "127.0.0.1", "targetPort": targetPort,
		}))
	if saved.ID == "" {
		t.Fatal("saved rule has no ID")
	}
	toggled := requireSavedRuleProductionResponse[forwardRuleAutoStartDTO](t,
		dispatchSavedRuleProduction(t, production, ctx, "forward_rule_set_auto_start", map[string]any{
			"id": saved.ID, "autoStart": true,
		}))
	if toggled.ID != saved.ID || !toggled.AutoStart {
		t.Fatalf("auto-start toggle = %+v, want %s enabled", toggled, saved.ID)
	}
	return saved
}

func recoverNowOutcomes(t *testing.T, production *Production) map[string]forwardRecoverOutcomeDTO {
	t.Helper()
	outcomes := requireSavedRuleProductionResponse[[]forwardRecoverOutcomeDTO](t,
		dispatchSavedRuleProduction(t, production, context.Background(), "forward_rule_recover_now", struct{}{}))
	byAsset := make(map[string]forwardRecoverOutcomeDTO, len(outcomes))
	for _, outcome := range outcomes {
		if _, duplicate := byAsset[outcome.AssetID]; duplicate {
			t.Fatalf("duplicate recovery outcome for asset %s", outcome.AssetID)
		}
		byAsset[outcome.AssetID] = outcome
	}
	return byAsset
}

// Recover-now never accepts an unknown host key on its own: the device
// outcome reports the failure without a session, the trust store stays empty,
// and the rule stays stopped. Only after the user confirms the key through
// the regular flow does recovery connect and bind the loopback listener.
func TestForwardRecoverNowDoesNotImplicitlyAcceptUnknownHostKey(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "recover-password", "secret")
	assetID := createConnectorTestAsset(t, production, "recover-host", "127.0.0.1", server.port(), "password", credID, "")
	ctx := context.Background()

	prodAsset, err := production.Services.Store.AssetCreate(ctx, store.AssetInput{
		Kind: "ssh", Name: "prod-host", OptionsJSON: "{}", Tags: "linux, production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := production.Services.Store.ForwardRuleSave(ctx, store.ForwardRuleInput{
		AssetID: prodAsset.ID, Name: "prod-db", ListenPort: freeSavedRuleProductionPort(t),
		TargetHost: "127.0.0.1", TargetPort: 3306, AutoStart: true,
	}); err != nil {
		t.Fatal(err)
	}

	upstream := startEchoListener(t)
	listenPort := freeSavedRuleProductionPort(t)
	saved := saveAutoStartRuleIPC(t, production, assetID, "recover-db", listenPort, echoListenerPort(t, upstream))

	// recover-now stays default-off until the switch is explicitly enabled.
	requireSavedRuleProductionError(t,
		dispatchSavedRuleProduction(t, production, ctx, "forward_rule_recover_now", struct{}{}), ipc.Code("conflict"))
	settings := requireSavedRuleProductionResponse[forwardRecoverySettingsDTO](t,
		dispatchSavedRuleProduction(t, production, ctx, "forward_rule_recovery_settings_set", map[string]any{"enabled": true}))
	if !settings.Enabled || settings.MaxConcurrentConnections != forward.MaxConcurrentRecoveryConnections || !settings.SkipProduction {
		t.Fatalf("recovery settings = %+v", settings)
	}

	byAsset := recoverNowOutcomes(t, production)
	prodOutcome, ok := byAsset[prodAsset.ID]
	if !ok || prodOutcome.Skipped == "" || prodOutcome.Error != "" || prodOutcome.SessionID != "" {
		t.Fatalf("production outcome = %+v (present=%v)", prodOutcome, ok)
	}
	unknown, ok := byAsset[assetID]
	if !ok || unknown.Error == "" || unknown.SessionID != "" || len(unknown.Started) != 0 {
		t.Fatalf("unknown-key outcome = %+v (present=%v)", unknown, ok)
	}
	if fingerprints := knownHostTestFingerprints(t, production); len(fingerprints) != 0 {
		t.Fatalf("recovery implicitly accepted an unknown host key: %+v", fingerprints)
	}
	if status := forwardRuleSettingsStatus(production, saved.ID); status != forward.StatusStopped {
		t.Fatalf("rule status after failed recovery = %q, want stopped", status)
	}

	pending := requireHostKeyPending(t,
		dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`), false)
	acceptHostKeyTest(t, production, pending)

	byAsset = recoverNowOutcomes(t, production)
	recovered, ok := byAsset[assetID]
	if !ok || recovered.Error != "" || recovered.SessionID == "" || len(recovered.Started) != 1 || recovered.Started[0] != saved.ID {
		t.Fatalf("recovered outcome = %+v (present=%v)", recovered, ok)
	}
	if prodOutcome, ok = byAsset[prodAsset.ID]; !ok || prodOutcome.Skipped == "" || prodOutcome.SessionID != "" {
		t.Fatalf("production asset recovered after explicit accept: %+v (present=%v)", prodOutcome, ok)
	}
	spec := forwardRuntimeSpecByID(t, production, saved.ID)
	if spec.ListenHost != "127.0.0.1" || spec.ListenPort != uint16(listenPort) || spec.Status != forward.StatusActive {
		t.Fatalf("recovered spec = %+v", spec)
	}
	conn := assertForwardEcho(t, spec, "recover-now")
	_ = conn.Close()
	if status := forwardRuleSettingsStatus(production, saved.ID); status != forward.StatusActive {
		t.Fatalf("rule status after recovery = %q, want active", status)
	}
}

// The connected-session hook starts the persisted auto-start rule when the
// user connects, fails it visibly on disconnect, and restarts the same fixed
// loopback port after a reconnect.
func TestForwardAutoStartRuleFollowsConnectedHookLifecycle(t *testing.T) {
	server := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "hook-password", "secret")
	assetID := createConnectorTestAsset(t, production, "hook-host", "127.0.0.1", server.port(), "password", credID, "")

	// Explicit trust first: the connected hook rides on sessions the user
	// confirmed, so no host key is ever accepted implicitly.
	pending := requireHostKeyPending(t,
		dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`), false)
	acceptHostKeyTest(t, production, pending)

	upstream := startEchoListener(t)
	listenPort := freeSavedRuleProductionPort(t)
	saved := saveAutoStartRuleIPC(t, production, assetID, "hook-db", listenPort, echoListenerPort(t, upstream))
	if status := forwardRuleSettingsStatus(production, saved.ID); status != forward.StatusStopped {
		t.Fatalf("rule started before any session: status = %q", status)
	}

	connected := connectHostKeyTestAsset(t, production, assetID)
	waitForCondition(t, 2*time.Second, func() bool {
		return forwardRuleSettingsStatus(production, saved.ID) == forward.StatusActive
	})
	spec := forwardRuntimeSpecByID(t, production, saved.ID)
	if spec.ListenHost != "127.0.0.1" || spec.SessionID != connected.ID || spec.Status != forward.StatusActive {
		t.Fatalf("connected-hook spec = %+v", spec)
	}
	conn := assertForwardEcho(t, spec, "connected-hook")
	_ = conn.Close()

	requireProductionNullHostKey(t,
		dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
	waitForCondition(t, 2*time.Second, func() bool {
		return forwardRuleSettingsStatus(production, saved.ID) == forward.StatusFailed
	})

	reconnected := connectHostKeyTestAsset(t, production, assetID)
	waitForCondition(t, 2*time.Second, func() bool {
		return forwardRuleSettingsStatus(production, saved.ID) == forward.StatusActive
	})
	spec = forwardRuntimeSpecByID(t, production, saved.ID)
	if spec.SessionID != reconnected.ID || spec.ListenHost != "127.0.0.1" || spec.ListenPort != uint16(listenPort) {
		t.Fatalf("restarted spec = %+v", spec)
	}
	conn = assertForwardEcho(t, spec, "reconnected-hook")
	_ = conn.Close()
	requireProductionNullHostKey(t,
		dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+reconnected.ID+`"}`))
}
