package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
)

func syncedStateRuleInput(pattern string) store.StateRuleInput {
	return store.StateRuleInput{Scope: store.StateRuleScopeGlobal, Region: staterule.RegionBottom, Pattern: pattern, State: "needs_me", Enabled: true}
}

func syncedRunbookInput(name string) store.RunbookTemplateInput {
	return store.RunbookTemplateInput{Name: name, Description: "restart", Steps: []store.RunbookTemplateStep{
		{ID: "s1", Name: "restart", Command: "systemctl restart api"},
	}}
}

func deviceSkills(t *testing.T, device *testDevice) *skills.Manager {
	t.Helper()
	manager, err := device.engine.skillManager(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func requireStateRule(t *testing.T, device *testDevice, id string, want bool) {
	t.Helper()
	_, found, err := device.db.StateRuleGet(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if found != want {
		t.Fatalf("state rule %s present=%v, want %v", id, found, want)
	}
}

func requireRunbookTemplate(t *testing.T, device *testDevice, id string, want bool) {
	t.Helper()
	_, err := device.db.RunbookTemplateGet(context.Background(), id)
	if found := err == nil; found != want {
		t.Fatalf("runbook template %s present=%v (err=%v), want %v", id, found, err, want)
	}
}

func requireSkill(t *testing.T, device *testDevice, name string, want bool) {
	t.Helper()
	overrides, err := deviceSkills(t, device).Overrides(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, override := range overrides {
		found = found || override.Name == name
	}
	if found != want {
		t.Fatalf("skill %s present=%v, want %v", name, found, want)
	}
}

// Rules, templates and skills created on one device reach another, and their deletions follow.
func TestStateRulesRunbookTemplatesAndSkillsSyncBetweenDevices(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	rule, err := deviceA.db.StateRuleSave(ctx, syncedStateRuleInput(`Continue\?`))
	if err != nil {
		t.Fatal(err)
	}
	template, err := deviceA.db.RunbookTemplateCreate(ctx, syncedRunbookInput("restart api"))
	if err != nil {
		t.Fatal(err)
	}
	description, body := "Check the disks", "Run df -h and report."
	if _, err := deviceSkills(t, deviceA).SaveOverride(ctx, skills.OverrideInput{Name: "disk-check", Description: &description, Body: &body}); err != nil {
		t.Fatal(err)
	}

	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	report := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if !report.changedKinds[KindStateRule] {
		t.Fatalf("changed kinds=%v, want state rules marked so the round pushes them to helpers", report.changedKinds)
	}
	requireStateRule(t, deviceB, rule.ID, true)
	requireRunbookTemplate(t, deviceB, template.ID, true)
	requireSkill(t, deviceB, "disk-check", true)
	received, err := deviceB.db.RunbookTemplateGet(ctx, template.ID)
	if err != nil {
		t.Fatal(err)
	}
	if received.Version != template.Version || received.Steps[0].Command != "systemctl restart api" {
		t.Fatalf("received template=%+v", received)
	}
	material, err := deviceSkills(t, deviceB).Material(ctx, "disk-check")
	if err != nil {
		t.Fatal(err)
	}
	if material.Body != body || material.Source != skills.SourceCustom {
		t.Fatalf("received skill=%+v", material)
	}

	// A second round on either side is quiet: the copies are identical.
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("identical copies pushed %d objects", report.Pushed)
	}

	if err := deviceA.db.StateRuleDelete(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.RunbookTemplateDelete(ctx, template.ID); err != nil {
		t.Fatal(err)
	}
	if err := deviceSkills(t, deviceA).DeleteOverride(ctx, "disk-check"); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	requireStateRule(t, deviceB, rule.ID, false)
	requireRunbookTemplate(t, deviceB, template.ID, false)
	requireSkill(t, deviceB, "disk-check", false)
}

// A newer local edit survives an older remote copy, and a remote edit with a newer revision replaces it.
func TestStateRuleSyncKeepsTheNewerRevision(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	rule, err := deviceA.db.StateRuleSave(ctx, syncedStateRuleInput(`Proceed\?`))
	if err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	edit := syncedStateRuleInput(`Proceed \(y/n\)\?`)
	edit.ID = rule.ID
	if _, err := deviceB.db.StateRuleSave(ctx, edit); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	got, found, err := deviceA.db.StateRuleGet(ctx, rule.ID)
	if err != nil || !found {
		t.Fatalf("rule found=%v err=%v", found, err)
	}
	if got.Pattern != edit.Pattern {
		t.Fatalf("pattern=%q, want the newer edit %q", got.Pattern, edit.Pattern)
	}
}

// Turning a switch off stops a kind in both directions without deleting either copy.
func TestStateRuleOptInOffStopsSync(t *testing.T) {
	ctx := context.Background()
	server := newTestSyncServer(t)
	userID, _ := seedTestSyncUser(t, server.db, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	if err := deviceB.db.SetStateRuleSyncOptIn(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	rule, err := deviceA.db.StateRuleSave(ctx, syncedStateRuleInput(`Overwrite\?`))
	if err != nil {
		t.Fatal(err)
	}
	local, err := deviceB.db.StateRuleSave(ctx, syncedStateRuleInput(`Retry\?`))
	if err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	requireStateRule(t, deviceB, rule.ID, false)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	requireStateRule(t, deviceA, local.ID, false)

	if err := deviceB.db.SetStateRuleSyncOptIn(ctx, userID, true); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	requireStateRule(t, deviceB, rule.ID, true)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	requireStateRule(t, deviceA, local.ID, true)
}

// An applied rule change runs the helper push hook for the owner; other kinds do not.
func TestApplyObjectsStateRulePushesToHelpers(t *testing.T) {
	instance := newTestInstance(t, false)
	var owners []string
	instance.service.SetStateRulesChanged(func(ctx context.Context) { owners = append(owners, ipc.OwnerID(ctx)) })
	ruleID := ids.New()
	rule := ApplyObject{ID: ruleID, Kind: KindStateRule, Payload: applyPayload(t, stateRuleObject{
		ID: ruleID, Name: "confirm", Scope: store.StateRuleScopeGlobal, Region: staterule.RegionBottom, Lines: 3,
		Pattern: `Continue\?`, State: "needs_me", Enabled: true, CreatedAt: 100, UpdatedAt: 100,
	})}
	result := mustApplyObjects(t, instance, applyGroupObject(t, ids.New(), "group", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if len(owners) != 0 {
		t.Fatalf("a group change pushed state rules to helpers: %v", owners)
	}
	result = mustApplyObjects(t, instance, rule)
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if len(owners) != 1 {
		t.Fatalf("hook calls=%d, want 1", len(owners))
	}
	result = mustApplyObjects(t, instance, rule)
	requireApplyResult(t, result.Objects[0], ApplyResultIdentical)
	if len(owners) != 1 {
		t.Fatalf("an identical rule pushed again: %d calls", len(owners))
	}
	result = mustApplyObjects(t, instance, ApplyObject{ID: ruleID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindStateRule, DeletedAt: 200})})
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if len(owners) != 2 {
		t.Fatalf("a rule deletion must push to helpers: %d calls", len(owners))
	}
	if _, found, err := instance.db.StateRuleGet(context.Background(), ruleID); err != nil || found {
		t.Fatalf("rule found=%v err=%v after its tombstone", found, err)
	}
}

// A host rule waits for its host: it is skipped with a warning until the asset exists.
func TestApplyObjectsHostStateRuleWaitsForItsHost(t *testing.T) {
	instance := newTestInstance(t, false)
	assetID, ruleID := ids.New(), ids.New()
	rule := ApplyObject{ID: ruleID, Kind: KindStateRule, Payload: applyPayload(t, stateRuleObject{
		ID: ruleID, Name: "host", Scope: store.StateRuleScopeHost, AssetID: assetID, Region: staterule.RegionBottom, Lines: 3,
		Pattern: `sudo`, State: "needs_me", Enabled: true, CreatedAt: 100, UpdatedAt: 100,
	})}
	result := mustApplyObjects(t, instance, rule)
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, assetID) {
		t.Fatalf("warning=%q, want it to name the missing host", result.Objects[0].Warning)
	}
	asset := ApplyObject{ID: assetID, Kind: KindAsset, Payload: applyPayload(t, assetObject{
		ID: assetID, Kind: "ssh", Name: "web", OptionsJSON: "{}", CreatedAt: 100, UpdatedAt: 100,
	})}
	result = mustApplyObjects(t, instance, rule, asset)
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	requireApplyResult(t, result.Objects[1], ApplyResultApplied)
}

// A host rule for a host deleted on this device is skipped for good; one whose host never arrives
// stops waiting after a bounded number of rounds, with a warning saying so.
func TestApplyObjectsHostStateRuleGivesUp(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	asset, err := instance.db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "retired", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.db.AssetDelete(ctx, asset.ID); err != nil {
		t.Fatal(err)
	}
	hostRule := func(assetID string) ApplyObject {
		id := ids.New()
		return ApplyObject{ID: id, Kind: KindStateRule, Payload: applyPayload(t, stateRuleObject{
			ID: id, Name: "host", Scope: store.StateRuleScopeHost, AssetID: assetID, Region: staterule.RegionBottom, Lines: 3,
			Pattern: `sudo`, State: "needs_me", Enabled: true, CreatedAt: 100, UpdatedAt: 100,
		})}
	}
	result := mustApplyObjects(t, instance, hostRule(asset.ID))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, "已在本机删除") {
		t.Fatalf("warning=%q, want the deleted host named as final", result.Objects[0].Warning)
	}

	missing := hostRule(ids.New())
	for round := 1; round <= maxPendingRounds; round++ {
		report := &SyncReport{}
		instance.service.engine.applyStateRuleObject(ctx, missing.Payload, report)
		if !report.hasUnresolvedRef(missing.ID) {
			t.Fatalf("round %d: the rule must stay pending while its host may still arrive", round)
		}
	}
	report := &SyncReport{}
	instance.service.engine.applyStateRuleObject(ctx, missing.Payload, report)
	if report.hasUnresolvedRef(missing.ID) || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "已跳过") {
		t.Fatalf("after %d rounds: pending=%v warnings=%v, want it recorded with a warning", maxPendingRounds, report.hasUnresolvedRef(missing.ID), report.Warnings)
	}
}

// A skill for a built-in this build does not ship must carry its own content.
func TestApplyObjectsRejectsInvalidSkills(t *testing.T) {
	instance := newTestInstance(t, false)
	body := "only a body"
	result := mustApplyObjects(t, instance,
		ApplyObject{ID: skills.SyncObjectID("no-description"), Kind: KindSkill, Payload: applyPayload(t, skillObject{
			ID: skills.SyncObjectID("no-description"), Name: "no-description", Body: &body, UpdatedAt: 100,
		})},
		ApplyObject{ID: skills.SyncObjectID("other-name"), Kind: KindSkill, Payload: applyPayload(t, skillObject{
			ID: skills.SyncObjectID("other-name"), Name: "mismatch", Body: &body, UpdatedAt: 100,
		})},
	)
	for _, entry := range result.Objects {
		requireApplyResult(t, entry, ApplyResultSkipped)
		if entry.Warning == "" {
			t.Fatalf("skipped skill %s must carry a warning", entry.ID)
		}
	}
}

// The browser collects the same canonical payload the engine pushes, so both sides agree on hashes and revisions.
// Memory leaves the device redacted and a cron job from before device binding is sent bound to this device.
func TestCollectKindObjectsMatchesEngineCollection(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	rule, err := instance.db.StateRuleSave(ctx, syncedStateRuleInput(`Continue\?`))
	if err != nil {
		t.Fatal(err)
	}
	template, err := instance.db.RunbookTemplateCreate(ctx, syncedRunbookInput("deploy"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := instance.service.engine.skillManager(ctx)
	if err != nil {
		t.Fatal(err)
	}
	description, body := "Check the disks", "Run df -h."
	skill, err := manager.SaveOverride(ctx, skills.OverrideInput{Name: "disk-check", Description: &description, Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	// A memory entry stored before a redaction rule existed still leaves the device redacted.
	memoryID := ids.New()
	if err := instance.db.GORM().Create(&model.MemoryEntry{
		ID: memoryID, Tenant: "local", Subject: "default", Topic: "db", Content: "login with password=hunter2",
		Version: 1, CreatedAt: 100, UpdatedAt: 200,
	}).Error; err != nil {
		t.Fatal(err)
	}
	jobs, err := instance.service.engine.cronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := jobs.Create(ctx, cron.Job{
		ID: "job-legacy", SessionID: "s", Prompt: "p", Schedule: "0 9 * * *", Timezone: "UTC", Enabled: true,
		Timeout: time.Minute, CreatedAt: now, UpdatedAt: now, ConfigUpdatedAt: now, NextRunAt: now.Add(time.Hour),
	}, 4); err != nil {
		t.Fatal(err)
	}
	if err := instance.db.GORM().Model(&model.CronJob{}).Where("id = ?", "job-legacy").Update("device_id", "").Error; err != nil {
		t.Fatal(err)
	}

	engineObjects, err := instance.service.engine.collectLocalObjects(ctx, &SyncReport{}, allKindOptIn(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	collectedPayload := map[string]string{}
	for _, check := range []struct {
		kind, id string
		updated  int64
	}{
		{KindStateRule, rule.ID, rule.UpdatedAt},
		{KindRunbookTemplate, template.ID, template.UpdatedAt},
		{KindSkill, skills.SyncObjectID("disk-check"), skill.UpdatedAt},
		{KindMemory, memoryID, 200},
		{KindCron, "job-legacy", now.UnixMilli()},
	} {
		collected, err := instance.service.CollectKindObjects(ctx, CollectKindObjectsRequest{Kind: check.kind})
		if err != nil {
			t.Fatal(err)
		}
		if len(collected.Objects) != 1 {
			t.Fatalf("%s collected %d objects", check.kind, len(collected.Objects))
		}
		object := collected.Objects[0]
		if object.ID != check.id || object.UpdatedAt != check.updated || object.Payload != string(engineObjects[check.id].plaintext) {
			t.Fatalf("%s collected %+v, engine payload %s", check.kind, object, engineObjects[check.id].plaintext)
		}
		collectedPayload[check.kind] = object.Payload
	}
	if strings.Contains(collectedPayload[KindMemory], "hunter2") || !strings.Contains(collectedPayload[KindMemory], `"redacted":true`) {
		t.Fatalf("memory payload %s, want the secret redacted", collectedPayload[KindMemory])
	}
	if !strings.Contains(collectedPayload[KindCron], `"deviceId":"`+jobs.Device().ID+`"`) {
		t.Fatalf("cron payload %s, want the unbound job sent as bound to this device %s", collectedPayload[KindCron], jobs.Device().ID)
	}
	if _, err := instance.service.CollectKindObjects(ctx, CollectKindObjectsRequest{Kind: KindAsset}); err == nil {
		t.Fatal("kinds with their own collect command must be rejected")
	}
}
