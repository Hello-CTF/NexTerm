package production

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type productionSettingsFixture struct {
	database *store.Store
	owner    context.Context
	other    context.Context
}

func TestProductionSettingsFixtureLifecycle(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, test := range []struct {
		name string
		run  func(*testing.T, *productionSettingsFixture)
	}{
		{name: "TestProductionSettingsDefaultOff", run: testProductionSettingsDefaultOff},
		{name: "TestProductionSettingsEmptyEnvironmentsMarshalAsEmptyArray", run: testProductionSettingsEmptyEnvironmentsMarshalAsEmptyArray},
		{name: "TestProductionSettingsSetAndGet", run: testProductionSettingsSetAndGet},
		{name: "TestProductionSettingsOwnerIsolation", run: testProductionSettingsOwnerIsolation},
		{name: "TestProductionSettingsInvalidInputFailClosed", run: testProductionSettingsInvalidInputFailClosed},
		{name: "TestProductionSettingsPatchSemantics", run: testProductionSettingsPatchSemantics},
		{name: "TestProductionSettingsAiTerminalConnectOptInRoundTrip", run: testProductionSettingsAiTerminalConnectOptInRoundTrip},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, newProductionSettingsFixture(t, database))
		})
	}
}

func newProductionSettingsFixture(t *testing.T, database *store.Store) *productionSettingsFixture {
	t.Helper()
	owner := ipc.WithUserID(t.Context(), t.Name()+"/owner-a")
	other := ipc.WithUserID(t.Context(), t.Name()+"/owner-b")
	return &productionSettingsFixture{database: database, owner: owner, other: other}
}

func (f *productionSettingsFixture) dispatch(t *testing.T, ctx context.Context, command string, input any) ipc.Response {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	if err := registerProductionSettingsCommands(dispatcher, f.database); err != nil {
		t.Fatal(err)
	}
	return dispatchExecBatchTest(dispatcher, ctx, command, input)
}

func (f *productionSettingsFixture) get(t *testing.T, ctx context.Context) productionSettingsDTO {
	t.Helper()
	response := f.dispatch(t, ctx, "production_settings_get", nil)
	var settings productionSettingsDTO
	requireStoreTestResponse(t, response, &settings)
	return settings
}

func (f *productionSettingsFixture) set(t *testing.T, ctx context.Context, input productionSettingsUpdateRequest) ipc.Response {
	t.Helper()
	return f.dispatch(t, ctx, "production_settings_set", input)
}

func testProductionSettingsDefaultOff(t *testing.T, fixture *productionSettingsFixture) {
	settings := fixture.get(t, fixture.owner)
	if len(settings.ProductionEnvironments) != 0 {
		t.Fatalf("default production environments = %v", settings.ProductionEnvironments)
	}
	if settings.CronConnectOptIn || settings.CronConnectProductionOptIn ||
		settings.BroadcastConnectOptIn || settings.BroadcastConnectProductionOptIn ||
		settings.TargetSetConnectOptIn || settings.TargetSetConnectProductionOptIn ||
		settings.AiTerminalConnectOptIn || settings.AiTerminalConnectProductionOptIn {
		t.Fatalf("default opt-ins are not all off: %+v", settings)
	}
}

func testProductionSettingsEmptyEnvironmentsMarshalAsEmptyArray(t *testing.T, fixture *productionSettingsFixture) {
	requireRawEmptyEnvironments := func(t *testing.T, response ipc.Response) {
		t.Helper()
		if !response.OK {
			t.Fatalf("dispatch failed: %+v", response)
		}
		if !strings.Contains(string(response.Data), `"productionEnvironments":[]`) {
			t.Fatalf("raw response must serialize an empty array, got %s", response.Data)
		}
	}
	// Never configured.
	requireRawEmptyEnvironments(t, fixture.dispatch(t, fixture.owner, "production_settings_get", nil))
	// Patch that only touches an opt-in leaves the environment list unset.
	requireRawEmptyEnvironments(t, fixture.set(t, fixture.owner, productionSettingsUpdateRequest{CronConnectOptIn: boolPointer(true)}))
	// Clearing the list persists and returns an empty array, not null.
	fixture.set(t, fixture.owner, productionSettingsUpdateRequest{ProductionEnvironments: stringSlicePointer([]string{"live"})})
	requireRawEmptyEnvironments(t, fixture.set(t, fixture.owner, productionSettingsUpdateRequest{ProductionEnvironments: stringSlicePointer([]string{})}))
	requireRawEmptyEnvironments(t, fixture.dispatch(t, fixture.owner, "production_settings_get", nil))
}

func testProductionSettingsSetAndGet(t *testing.T, fixture *productionSettingsFixture) {
	response := fixture.set(t, fixture.owner, productionSettingsUpdateRequest{
		ProductionEnvironments: stringSlicePointer([]string{"live", "staging"}),
		CronConnectOptIn:       boolPointer(true),
		TargetSetConnectOptIn:  boolPointer(true),
	})
	var settings productionSettingsDTO
	requireStoreTestResponse(t, response, &settings)
	if len(settings.ProductionEnvironments) != 2 || settings.ProductionEnvironments[0] != "live" || settings.ProductionEnvironments[1] != "staging" {
		t.Fatalf("saved production environments = %v", settings.ProductionEnvironments)
	}
	if !settings.CronConnectOptIn || !settings.TargetSetConnectOptIn {
		t.Fatalf("saved opt-ins = %+v", settings)
	}
	if settings.CronConnectProductionOptIn || settings.BroadcastConnectOptIn || settings.BroadcastConnectProductionOptIn || settings.TargetSetConnectProductionOptIn ||
		settings.AiTerminalConnectOptIn || settings.AiTerminalConnectProductionOptIn {
		t.Fatalf("unset opt-ins changed: %+v", settings)
	}
	// Built-in production/prod still apply alongside the configured list.
	if production, err := guard.IsProductionEnvironment(fixture.owner, fixture.database, "production"); err != nil || !production {
		t.Fatalf("built-in production lost: %v, %v", production, err)
	}
	if production, err := guard.IsProductionEnvironment(fixture.owner, fixture.database, "live"); err != nil || !production {
		t.Fatalf("configured live missing: %v, %v", production, err)
	}
	// The cron connector reads the same per-user key.
	if optIn, err := cronConnectSetting(fixture.owner, fixture.database, cron.ConnectOptInSettingKeyForUser(fixture.owner)); err != nil || !optIn {
		t.Fatalf("cron opt-in not persisted: %v, %v", optIn, err)
	}
}

func testProductionSettingsOwnerIsolation(t *testing.T, fixture *productionSettingsFixture) {
	fixture.set(t, fixture.owner, productionSettingsUpdateRequest{
		ProductionEnvironments: stringSlicePointer([]string{"live"}),
		CronConnectOptIn:       boolPointer(true),
	})
	other := fixture.get(t, fixture.other)
	if len(other.ProductionEnvironments) != 0 || other.CronConnectOptIn {
		t.Fatalf("cross-owner settings leaked: %+v", other)
	}
	if production, err := guard.IsProductionEnvironment(fixture.other, fixture.database, "live"); err != nil || production {
		t.Fatalf("cross-owner production classification: %v, %v", production, err)
	}
}

func testProductionSettingsInvalidInputFailClosed(t *testing.T, fixture *productionSettingsFixture) {
	for _, test := range []struct {
		name  string
		input productionSettingsUpdateRequest
	}{
		{name: "empty name", input: productionSettingsUpdateRequest{ProductionEnvironments: stringSlicePointer([]string{"live", ""})}},
		{name: "whitespace name", input: productionSettingsUpdateRequest{ProductionEnvironments: stringSlicePointer([]string{"   "})}},
		{name: "over-long name", input: productionSettingsUpdateRequest{ProductionEnvironments: stringSlicePointer([]string{strings.Repeat("x", 65)})}},
		{name: "too many names", input: productionSettingsUpdateRequest{ProductionEnvironments: stringSlicePointer(manyEnvironmentNames(33))}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.set(t, fixture.owner, test.input)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
				t.Fatalf("invalid input response = %+v, want bad_param", response)
			}
			settings := fixture.get(t, fixture.owner)
			if len(settings.ProductionEnvironments) != 0 {
				t.Fatalf("invalid input was persisted: %v", settings.ProductionEnvironments)
			}
		})
	}
}

func testProductionSettingsPatchSemantics(t *testing.T, fixture *productionSettingsFixture) {
	fixture.set(t, fixture.owner, productionSettingsUpdateRequest{
		ProductionEnvironments: stringSlicePointer([]string{"live"}),
		CronConnectOptIn:       boolPointer(true),
	})
	fixture.set(t, fixture.owner, productionSettingsUpdateRequest{BroadcastConnectOptIn: boolPointer(true)})
	settings := fixture.get(t, fixture.owner)
	if len(settings.ProductionEnvironments) != 1 || settings.ProductionEnvironments[0] != "live" || !settings.CronConnectOptIn || !settings.BroadcastConnectOptIn {
		t.Fatalf("patched settings = %+v", settings)
	}
	fixture.set(t, fixture.owner, productionSettingsUpdateRequest{CronConnectOptIn: boolPointer(false)})
	settings = fixture.get(t, fixture.owner)
	if settings.CronConnectOptIn {
		t.Fatalf("opt-in not cleared: %+v", settings)
	}
}

func stringSlicePointer(values []string) *[]string { return &values }
func boolPointer(value bool) *bool                 { return &value }

func manyEnvironmentNames(count int) []string {
	names := make([]string, count)
	for index := range names {
		names[index] = strings.Repeat("x", index+1)
	}
	return names
}

// The AI terminal connect surface is configurable per owner through the same
// public settings IPC: the patch persists to the surface's own per-user keys
// and never touches the other surfaces' opt-ins.
func testProductionSettingsAiTerminalConnectOptInRoundTrip(t *testing.T, fixture *productionSettingsFixture) {
	response := fixture.set(t, fixture.owner, productionSettingsUpdateRequest{
		AiTerminalConnectOptIn:           boolPointer(true),
		AiTerminalConnectProductionOptIn: boolPointer(true),
	})
	var settings productionSettingsDTO
	requireStoreTestResponse(t, response, &settings)
	if !settings.AiTerminalConnectOptIn || !settings.AiTerminalConnectProductionOptIn {
		t.Fatalf("saved AI terminal opt-ins = %+v", settings)
	}
	if settings.CronConnectOptIn || settings.BroadcastConnectOptIn || settings.TargetSetConnectOptIn {
		t.Fatalf("other surfaces changed by the AI terminal patch: %+v", settings)
	}
	// The AI terminal connector reads the same per-user keys.
	if optIn, err := cronConnectSetting(fixture.owner, fixture.database, aiTerminalConnectOptInSettingKeyForUser(fixture.owner)); err != nil || !optIn {
		t.Fatalf("AI terminal opt-in not persisted: %v, %v", optIn, err)
	}
	if optIn, err := cronConnectSetting(fixture.owner, fixture.database, aiTerminalConnectProductionOptInSettingKeyForUser(fixture.owner)); err != nil || !optIn {
		t.Fatalf("AI terminal production opt-in not persisted: %v, %v", optIn, err)
	}
	other := fixture.get(t, fixture.other)
	if other.AiTerminalConnectOptIn || other.AiTerminalConnectProductionOptIn {
		t.Fatalf("cross-owner AI terminal opt-ins leaked: %+v", other)
	}
}
