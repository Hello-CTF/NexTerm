//go:build darwin || linux

package production

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func newForwardIPCFixture(t *testing.T, upstream string) (*forwardRuntimeFixture, *ipc.Dispatcher) {
	t.Helper()
	fixture := newForwardRuntimeFixture(t, upstream)
	fixture.runtime.recoverySettings = &forwardRecoverySettingsStore{path: filepath.Join(t.TempDir(), "forward-recovery.json")}
	dispatcher := ipc.NewDispatcher()
	if err := fixture.runtime.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return fixture, dispatcher
}

func recoverySettingsGet(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context) forwardRecoverySettingsDTO {
	t.Helper()
	return decodeFSTransferTest[forwardRecoverySettingsDTO](t, dispatchFSTransferTest(dispatcher, ctx, "forward_rule_recovery_settings_get", struct{}{}, nil))
}

func recoverySettingsSet(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, enabled bool) forwardRecoverySettingsDTO {
	t.Helper()
	return decodeFSTransferTest[forwardRecoverySettingsDTO](t, dispatchFSTransferTest(dispatcher, ctx, "forward_rule_recovery_settings_set", forwardRecoverySettingsSetRequest{Enabled: enabled}, nil))
}

func recoverNow(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context) ipc.Response {
	t.Helper()
	return dispatchFSTransferTest(dispatcher, ctx, "forward_rule_recover_now", struct{}{}, nil)
}

func TestForwardRecoverySettingsDefaultOffPersistenceAndCorruptFailClosed(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))

	if got := recoverySettingsGet(t, dispatcher, fixture.ctx); got.Enabled || got.MaxConcurrentConnections != forward.MaxConcurrentRecoveryConnections || !got.SkipProduction {
		t.Fatalf("default recovery settings = %+v", got)
	}
	if got := recoverySettingsSet(t, dispatcher, fixture.ctx, true); !got.Enabled {
		t.Fatalf("enabled recovery settings = %+v", got)
	}

	// A new runtime over the same path keeps the switch (restart persistence).
	restarted := newForwardRuntime(forwardRuntimeConfig{
		Forward: fixture.forward, Sessions: fixture.manager, Store: fixture.db,
		RecoverySettingsPath: fixture.runtime.recoverySettings.path,
	})
	restartedDispatcher := ipc.NewDispatcher()
	if err := restarted.registerCommands(restartedDispatcher); err != nil {
		t.Fatal(err)
	}
	if got := recoverySettingsGet(t, restartedDispatcher, fixture.ctx); !got.Enabled {
		t.Fatalf("persisted recovery settings = %+v", got)
	}

	// A corrupt file fails closed to disabled.
	if err := os.WriteFile(fixture.runtime.recoverySettings.path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := recoverySettingsGet(t, dispatcher, fixture.ctx); got.Enabled {
		t.Fatalf("corrupt recovery settings = %+v, want disabled", got)
	}
}

func TestForwardRecoverySettingsPartialDecodeFailsClosed(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))
	fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	// A type error after a successfully decoded field must not keep the
	// partially decoded value: any decode failure fails closed to disabled.
	if err := os.WriteFile(fixture.runtime.recoverySettings.path, []byte(`{"enabled":true,"enabled":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := recoverySettingsGet(t, dispatcher, fixture.ctx); got.Enabled {
		t.Fatalf("partially decoded recovery settings = %+v, want disabled", got)
	}
	response := recoverNow(t, dispatcher, fixture.ctx)
	if response.OK || response.Error == nil || response.Error.Code != ipc.Code("conflict") {
		t.Fatalf("recover_now with corrupt settings = %+v", response)
	}
	sessions, err := fixture.manager.ListSessionsFor(fixture.ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("corrupt-settings recover_now dialed: %d, %v", len(sessions), err)
	}
}

func TestForwardRuleSetAutoStartOwnerScoped(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))
	rule := fixture.saveAutoStartRule(t, fixture.asset.ID, false)

	updated := decodeFSTransferTest[forwardRuleAutoStartDTO](t, dispatchFSTransferTest(dispatcher, fixture.ctx, "forward_rule_set_auto_start", forwardRuleSetAutoStartRequest{ID: rule.ID, AutoStart: true}, nil))
	if updated.ID != rule.ID || !updated.AutoStart {
		t.Fatalf("set_auto_start = %+v", updated)
	}
	row, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID)
	if err != nil || !row.AutoStart {
		t.Fatalf("persisted autoStart = %v, %v", row.AutoStart, err)
	}

	ownerB := ipc.WithUserID(context.Background(), "owner-b")
	denied := dispatchFSTransferTest(dispatcher, ownerB, "forward_rule_set_auto_start", forwardRuleSetAutoStartRequest{ID: rule.ID, AutoStart: false}, nil)
	if denied.OK || denied.Error == nil || denied.Error.Code != ipc.CodeNotFound {
		t.Fatalf("cross-owner set_auto_start = %+v", denied)
	}
	row, err = fixture.db.ForwardRuleGet(fixture.ctx, rule.ID)
	if err != nil || !row.AutoStart {
		t.Fatalf("cross-owner toggle changed the rule: %v, %v", row.AutoStart, err)
	}

	settings := decodeFSTransferTest[[]forwardRuleSettingsDTO](t, dispatchFSTransferTest(dispatcher, fixture.ctx, "forward_rule_settings_list", struct{}{}, nil))
	if len(settings) != 1 || settings[0].ID != rule.ID || !settings[0].AutoStart || settings[0].Status != forward.StatusStopped {
		t.Fatalf("rule settings = %+v", settings)
	}
}

func TestForwardRecoverNowRequiresEnabled(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))
	fixture.saveAutoStartRule(t, fixture.asset.ID, true)

	response := recoverNow(t, dispatcher, fixture.ctx)
	if response.OK || response.Error == nil || response.Error.Code != ipc.Code("conflict") {
		t.Fatalf("recover_now disabled = %+v", response)
	}
	sessions, err := fixture.manager.ListSessionsFor(fixture.ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("disabled recover_now dialed: %d, %v", len(sessions), err)
	}
}

func TestForwardRecoverNowStartsOwnerDevices(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))
	recoverySettingsSet(t, dispatcher, fixture.ctx, true)

	plain := fixture.asset
	manualAsset, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{Kind: "ssh", Name: "manual-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	productionAsset, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{Kind: "ssh", Name: "prod-host", OptionsJSON: "{}", Tags: "linux, production"})
	if err != nil {
		t.Fatal(err)
	}
	autoRule := fixture.saveAutoStartRule(t, plain.ID, true)
	fixture.saveAutoStartRule(t, manualAsset.ID, false)
	fixture.saveAutoStartRule(t, productionAsset.ID, true)

	outcomes := decodeFSTransferTest[[]forwardRecoverOutcomeDTO](t, recoverNow(t, dispatcher, fixture.ctx))
	if len(outcomes) != 2 {
		t.Fatalf("recover outcomes = %+v", outcomes)
	}
	byAsset := map[string]forwardRecoverOutcomeDTO{}
	for _, outcome := range outcomes {
		byAsset[outcome.AssetID] = outcome
	}
	started := byAsset[plain.ID]
	if started.Error != "" || started.Skipped != "" || started.SessionID == "" || len(started.Started) != 1 || started.Started[0] != autoRule.ID {
		t.Fatalf("started outcome = %+v", started)
	}
	if skipped := byAsset[productionAsset.ID]; skipped.Skipped != "生产环境资产不参与启动恢复" {
		t.Fatalf("production outcome = %+v", skipped)
	}
	// Devices without an auto-start rule are not enumerated at all.
	if _, enumerated := byAsset[manualAsset.ID]; enumerated {
		t.Fatalf("manual device was enumerated: %+v", byAsset[manualAsset.ID])
	}
	conn := assertForwardEcho(t, fixture.forward.ListFor(fixture.ctx)[0], "recovered-now")
	_ = conn.Close()

	// Another account's recover-now enumerates only its own devices.
	ownerB := ipc.WithUserID(context.Background(), "owner-b")
	otherOutcomes := decodeFSTransferTest[[]forwardRecoverOutcomeDTO](t, recoverNow(t, dispatcher, ownerB))
	if len(otherOutcomes) != 0 {
		t.Fatalf("cross-owner outcomes = %+v", otherOutcomes)
	}
}

func TestForwardRecoverNowAfterShutdownRefused(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))
	recoverySettingsSet(t, dispatcher, fixture.ctx, true)
	fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	if err := fixture.runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	response := recoverNow(t, dispatcher, fixture.ctx)
	if response.OK || response.Error == nil || !strings.Contains(response.Error.Error(), "已关闭") {
		t.Fatalf("recover_now after shutdown = %+v", response)
	}
}

func TestForwardRuntimeStartNeverDialsWithRecoveryEnabled(t *testing.T) {
	fixture, dispatcher := newForwardIPCFixture(t, startEchoListener(t))
	recoverySettingsSet(t, dispatcher, fixture.ctx, true)
	fixture.saveAutoStartRule(t, fixture.asset.ID, true)
	if err := fixture.runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fixture.runtime.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	sessions, err := fixture.manager.ListSessionsFor(fixture.ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("Start dialed with recovery enabled: %d, %v", len(sessions), err)
	}
}
