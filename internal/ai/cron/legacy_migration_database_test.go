package cron

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner/legacy"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

func legacyMigrationConversation(id, ownerID string) store.ConversationRow {
	return store.ConversationRow{ID: id, ScopeJSON: "{}", CreatedAt: 10, UpdatedAt: 10, OwnerID: ownerID}
}

func legacyMigrationRun(id, conversationID, ownerID string) store.RunRow {
	return store.RunRow{ID: id, ConversationID: conversationID, Status: store.RunStatusRunning, Source: "cron", CreatedAt: 20, UpdatedAt: 20, OwnerID: ownerID}
}

func legacyMigrationCronJob(id, sessionID string) store.CronJob {
	return store.CronJob{
		ID: id, SessionID: sessionID, Prompt: "migration-test-prompt", Schedule: "* * * * *", Timezone: "UTC",
		Enabled: true, TimeoutMS: 1000, CreatedAt: 30, UpdatedAt: 30, Revision: 1, NextRunAt: 1,
	}
}

func createLegacyMigrationRow(t *testing.T, database *gorm.DB, value any) {
	t.Helper()
	if err := database.Create(value).Error; err != nil {
		t.Fatal(err)
	}
}

func requireLegacyMigrationOwner(t *testing.T, database *gorm.DB, model any, id, want string) {
	t.Helper()
	var ownerID string
	if err := database.Model(model).Select("owner_id").Where("id = ?", id).Scan(&ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if ownerID != want {
		t.Fatalf("%T owner = %q, want %q", model, ownerID, want)
	}
}

func TestOwnerlessAIMigrationDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM().WithContext(ctx)
	for _, row := range []store.ConversationRow{
		legacyMigrationConversation("conv-a", "owner-a"),
		legacyMigrationConversation("conv-b", "owner-b"),
		legacyMigrationConversation("conv-orphan", ""),
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}

	jobA := legacyMigrationCronJob("job-a", "conv-a")
	jobDisabledOwned := legacyMigrationCronJob("job-disabled-owned", "conv-a")
	jobDisabledOwned.Enabled = false
	jobOwnedB := legacyMigrationCronJob("job-owned-b", "conv-a")
	jobOwnedB.OwnerID, jobOwnedB.OwnerKind, jobOwnedB.UpdatedAt = "owner-b", string(OwnerKindAccount), 99
	jobOpenOwned := legacyMigrationCronJob("job-open-owned", "conv-a")
	jobOpenOwned.OwnerKind = string(OwnerKindOpen)
	jobOpenOrphan := legacyMigrationCronJob("job-open-orphan", "conv-orphan")
	jobOpenOrphan.OwnerKind = string(OwnerKindOpen)
	jobOrphan := legacyMigrationCronJob("job-orphan", "conv-orphan")
	jobMissing := legacyMigrationCronJob("job-missing", "missing-conversation")
	jobDisabledOrphan := legacyMigrationCronJob("job-disabled-orphan", "conv-orphan")
	jobDisabledOrphan.Enabled = false
	jobDisabledOrphan.LastError = "keep-existing-error"
	jobTombstoned := legacyMigrationCronJob("job-tombstoned", "conv-a")
	for _, row := range []store.CronJob{
		jobA, jobDisabledOwned, jobOwnedB, jobOpenOwned, jobOpenOrphan, jobOrphan, jobMissing, jobDisabledOrphan, jobTombstoned,
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}

	for _, row := range []store.RunRow{
		legacyMigrationRun("run-a", "conv-a", ""),
		legacyMigrationRun("run-b", "conv-b", ""),
		legacyMigrationRun("run-owned-b", "conv-a", "owner-b"),
		legacyMigrationRun("run-orphan", "conv-orphan", ""),
		legacyMigrationRun("run-tombstoned", "conv-a", ""),
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}
	for _, row := range []store.MessageRow{
		{ID: "message-a", ConversationID: "conv-a", Role: "user", ContentJSON: "{}", CreatedAt: 40, Seq: 1},
		{ID: "message-owned-b", ConversationID: "conv-a", Role: "user", ContentJSON: "{}", CreatedAt: 41, Seq: 2, OwnerID: "owner-b"},
		{ID: "message-orphan", ConversationID: "conv-orphan", Role: "user", ContentJSON: "{}", CreatedAt: 42, Seq: 1},
		{ID: "message-tombstoned", ConversationID: "conv-a", Role: "user", ContentJSON: "{}", CreatedAt: 43, Seq: 3},
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}
	for _, row := range []store.RunEventRow{
		{RunID: "run-a", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 50},
		{RunID: "run-a", Seq: 2, Type: "delta", PayloadJSON: "{}", CreatedAt: 51},
		{RunID: "run-owned-b", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 52},
		{RunID: "run-orphan", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 53},
		{RunID: "run-tombstoned", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 54},
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}
	for _, row := range []store.HitlRunRow{
		{ID: "run-a", Data: []byte("{}"), UpdatedAt: 60},
		{ID: "run-orphan", Data: []byte("{}"), UpdatedAt: 61},
		{ID: "run-tombstoned", Data: []byte("{}"), UpdatedAt: 62},
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}
	for _, row := range []store.CheckpointRow{
		{ID: "run-a", Data: []byte("{}"), UpdatedAt: 70},
		{ID: "run-orphan", Data: []byte("{}"), UpdatedAt: 71},
		{ID: "takeover:recovery:job", Data: []byte("{}"), UpdatedAt: 72},
		{ID: "run-tombstoned", Data: []byte("{}"), UpdatedAt: 73},
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}
	for _, row := range []store.SyncTombstone{
		{ID: "job-tombstoned", Kind: "cron_job", DeletedAt: 123, OwnerID: "owner-a"},
		{ID: "run-tombstoned", Kind: "ai_run", DeletedAt: 123, OwnerID: "owner-a"},
		{ID: "message-tombstoned", Kind: "ai_message", DeletedAt: 123, OwnerID: "owner-a"},
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}

	report, err := MigrateOwnerlessAI(ctx, gormDB, false)
	if err != nil {
		t.Fatal(err)
	}
	want := OwnerlessMigrationReport{
		CronJobsAssigned: 3, CronJobsQuarantined: 5, RunsAssigned: 2, MessagesAssigned: 1,
		RunEventsAssigned: 3, HitlRunsAssigned: 1, CheckpointsAssigned: 1, Unattributed: 17,
	}
	if report != want {
		t.Fatalf("migration report = %+v, want %+v", report, want)
	}

	for _, test := range []struct {
		model   any
		id      string
		ownerID string
	}{
		{&store.CronJob{}, "job-a", "owner-a"},
		{&store.CronJob{}, "job-disabled-owned", "owner-a"},
		{&store.CronJob{}, "job-open-owned", "owner-a"},
		{&store.CronJob{}, "job-owned-b", "owner-b"},
		{&store.CronJob{}, "job-tombstoned", ""},
		{&store.RunRow{}, "run-a", "owner-a"},
		{&store.RunRow{}, "run-b", "owner-b"},
		{&store.RunRow{}, "run-owned-b", "owner-b"},
		{&store.RunRow{}, "run-orphan", ""},
		{&store.RunRow{}, "run-tombstoned", ""},
		{&store.MessageRow{}, "message-a", "owner-a"},
		{&store.MessageRow{}, "message-owned-b", "owner-b"},
		{&store.MessageRow{}, "message-orphan", ""},
		{&store.MessageRow{}, "message-tombstoned", ""},
		{&store.HitlRunRow{}, "run-a", "owner-a"},
		{&store.HitlRunRow{}, "run-orphan", ""},
		{&store.HitlRunRow{}, "run-tombstoned", ""},
		{&store.CheckpointRow{}, "run-a", "owner-a"},
		{&store.CheckpointRow{}, "run-orphan", ""},
		{&store.CheckpointRow{}, "run-tombstoned", ""},
		{&store.CheckpointRow{}, "takeover:recovery:job", ""},
	} {
		requireLegacyMigrationOwner(t, gormDB, test.model, test.id, test.ownerID)
	}
	for _, row := range []store.RunEventRow{
		{RunID: "run-a", Seq: 1}, {RunID: "run-a", Seq: 2}, {RunID: "run-owned-b", Seq: 1}, {RunID: "run-orphan", Seq: 1}, {RunID: "run-tombstoned", Seq: 1},
	} {
		var stored store.RunEventRow
		if err := gormDB.Where("run_id = ? AND seq = ?", row.RunID, row.Seq).First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		wantOwner := ""
		switch stored.RunID {
		case "run-a":
			wantOwner = "owner-a"
		case "run-owned-b":
			wantOwner = "owner-b"
		}
		if stored.OwnerID != wantOwner {
			t.Fatalf("event %s/%d owner = %q, want %q", stored.RunID, stored.Seq, stored.OwnerID, wantOwner)
		}
	}

	for id, want := range map[string]struct {
		ownerID   string
		ownerKind OwnerKind
		enabled   bool
		lastError string
	}{
		"job-a":               {ownerID: "owner-a", ownerKind: OwnerKindAccount, enabled: true},
		"job-disabled-owned":  {ownerID: "owner-a", ownerKind: OwnerKindAccount, enabled: false},
		"job-open-owned":      {ownerID: "owner-a", ownerKind: OwnerKindAccount, enabled: true},
		"job-open-orphan":     {ownerKind: OwnerKindLegacy, enabled: false, lastError: ErrOwnerlessLegacy.Error()},
		"job-orphan":          {ownerKind: OwnerKindLegacy, enabled: false, lastError: ErrOwnerlessLegacy.Error()},
		"job-missing":         {ownerKind: OwnerKindLegacy, enabled: false, lastError: ErrOwnerlessLegacy.Error()},
		"job-disabled-orphan": {ownerKind: OwnerKindLegacy, enabled: false, lastError: "keep-existing-error"},
		"job-tombstoned":      {ownerKind: OwnerKindLegacy, enabled: false, lastError: ErrOwnerlessLegacy.Error()},
	} {
		var stored store.CronJob
		if err := gormDB.Where("id = ?", id).First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.OwnerID != want.ownerID || OwnerKind(stored.OwnerKind) != want.ownerKind || stored.Enabled != want.enabled {
			t.Fatalf("cron %s = owner %q/%q enabled %v, want %+v", id, stored.OwnerID, stored.OwnerKind, stored.Enabled, want)
		}
		if want.lastError != "" && stored.LastError != want.lastError {
			t.Fatalf("cron %s last error = %q, want %q", id, stored.LastError, want.lastError)
		}
	}
	var preserved store.CronJob
	if err := gormDB.Where("id = ?", "job-owned-b").First(&preserved).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.OwnerID != "owner-b" || preserved.UpdatedAt != 99 {
		t.Fatalf("existing per-owner cron changed: %+v", preserved)
	}
	requireLegacyMigrationOwner(t, gormDB, &store.ConversationRow{}, "conv-orphan", "")
	var tombstone store.SyncTombstone
	if err := gormDB.Where("id = ?", "run-tombstoned").First(&tombstone).Error; err != nil {
		t.Fatal(err)
	}
	if tombstone.DeletedAt != 123 || tombstone.OwnerID != "owner-a" {
		t.Fatalf("tombstone changed: %+v", tombstone)
	}

	var ledger []store.AIOwnerMigration
	if err := gormDB.Find(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 16 {
		t.Fatalf("ledger rows = %d, want 16", len(ledger))
	}
	addedAt := make(map[string]int64, len(ledger))
	for _, row := range ledger {
		if row.Migration != ownerMigrationVersion || row.AddedAt <= 0 {
			t.Fatalf("invalid ledger marker: %+v", row)
		}
		addedAt[row.Kind+"/"+row.RecordID] = row.AddedAt
	}
	if ownerID := ledgerOwner(ledger, "ai_run_event", "run-owned-b:1"); ownerID != "owner-b" {
		t.Fatalf("event ledger owner = %q, want parent run owner", ownerID)
	}

	repeated, err := MigrateOwnerlessAI(ctx, gormDB, false)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != (OwnerlessMigrationReport{Unattributed: 17}) {
		t.Fatalf("repeated migration report = %+v", repeated)
	}
	var repeatedLedger []store.AIOwnerMigration
	if err := gormDB.Find(&repeatedLedger).Error; err != nil {
		t.Fatal(err)
	}
	if len(repeatedLedger) != len(ledger) {
		t.Fatalf("reentry ledger rows = %d, want %d", len(repeatedLedger), len(ledger))
	}
	for _, row := range repeatedLedger {
		if addedAt[row.Kind+"/"+row.RecordID] != row.AddedAt {
			t.Fatalf("reentry changed ledger marker %s/%s", row.Kind, row.RecordID)
		}
	}
	var disabled store.CronJob
	if err := gormDB.Where("id = ?", "job-disabled-owned").First(&disabled).Error; err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled {
		t.Fatal("migration re-enabled a previously disabled cron job")
	}
}

func ledgerOwner(rows []store.AIOwnerMigration, kind, recordID string) string {
	for _, row := range rows {
		if row.Kind == kind && row.RecordID == recordID {
			return row.OwnerID
		}
	}
	return ""
}

func TestOwnerlessAIMigrationSkipsTombstonedConversationRunDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM().WithContext(ctx)
	createLegacyMigrationRow(t, gormDB, &store.ConversationRow{ID: "conv-deleted", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: "owner-a"})
	createLegacyMigrationRow(t, gormDB, &store.RunRow{ID: "run-existing", ConversationID: "conv-deleted", Status: store.RunStatusRunning, Source: "cron", CreatedAt: 2, UpdatedAt: 2, OwnerID: "owner-a"})
	createLegacyMigrationRow(t, gormDB, &store.RunEventRow{RunID: "run-existing", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 3})
	createLegacyMigrationRow(t, gormDB, &store.HitlRunRow{ID: "run-existing", Data: []byte("{}"), UpdatedAt: 4})
	createLegacyMigrationRow(t, gormDB, &store.CheckpointRow{ID: "run-existing", Data: []byte("{}"), UpdatedAt: 5})
	createLegacyMigrationRow(t, gormDB, &store.SyncTombstone{ID: "conv-deleted", Kind: "ai_conversation", DeletedAt: 100, OwnerID: "owner-a"})

	for range 2 {
		report, err := MigrateOwnerlessAI(ctx, gormDB, false)
		if err != nil {
			t.Fatal(err)
		}
		if report != (OwnerlessMigrationReport{Unattributed: 3}) {
			t.Fatalf("tombstoned conversation migration report = %+v", report)
		}
		requireLegacyMigrationOwner(t, gormDB, &store.RunRow{}, "run-existing", "owner-a")
		requireLegacyMigrationOwner(t, gormDB, &store.HitlRunRow{}, "run-existing", "")
		requireLegacyMigrationOwner(t, gormDB, &store.CheckpointRow{}, "run-existing", "")
		var event store.RunEventRow
		if err := gormDB.Where("run_id = ? AND seq = ?", "run-existing", 1).First(&event).Error; err != nil {
			t.Fatal(err)
		}
		if event.OwnerID != "" {
			t.Fatalf("tombstoned conversation descendant event was assigned to %q", event.OwnerID)
		}
		var ledgerCount int64
		if err := gormDB.Model(&store.AIOwnerMigration{}).Count(&ledgerCount).Error; err != nil {
			t.Fatal(err)
		}
		if ledgerCount != 0 {
			t.Fatalf("tombstoned conversation descendants produced %d assignment ledger rows", ledgerCount)
		}
	}
}

func TestOwnerMigrationsCoexistDatabase(t *testing.T) {
	for _, order := range []string{"cron-first", "settings-first"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			database := dbtest.NewFixture(t).OpenStore(t)
			gormDB := database.GORM().WithContext(ctx)
			createLegacyMigrationRow(t, gormDB, &store.AppUser{ID: "first-admin", Username: "first-admin", Role: "superadmin", State: "active", PasswordHash: "test", CreatedAt: 1, UpdatedAt: 1})
			encoded, err := json.Marshal(guard.Config{Mode: guard.ReadOnly, DangerRules: []string{"legacy-danger"}})
			if err != nil {
				t.Fatal(err)
			}
			createLegacyMigrationRow(t, gormDB, &store.Setting{Key: guard.PermissionSettingKey, Value: string(encoded), UpdatedAt: 100})
			createLegacyMigrationRow(t, gormDB, &store.ConversationRow{ID: "conv-coexist", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: "owner-a"})
			createLegacyMigrationRow(t, gormDB, &store.CronJob{ID: "job-coexist", SessionID: "conv-coexist", Prompt: "coexist", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: 1})

			var cronReport OwnerlessMigrationReport
			var settingsReport legacy.Report
			if order == "cron-first" {
				cronReport, err = MigrateOwnerlessAI(ctx, gormDB, false)
				if err != nil {
					t.Fatal(err)
				}
				settingsReport, err = legacy.Migrate(ctx, gormDB, false)
			} else {
				settingsReport, err = legacy.Migrate(ctx, gormDB, false)
				if err != nil {
					t.Fatal(err)
				}
				cronReport, err = MigrateOwnerlessAI(ctx, gormDB, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if cronReport != (OwnerlessMigrationReport{CronJobsAssigned: 1}) {
				t.Fatalf("cron migration report = %+v", cronReport)
			}
			if settingsReport != (legacy.Report{Permissions: legacy.ItemReport{Migrated: 1}, LedgerEntries: 1}) {
				t.Fatalf("settings migration report = %+v", settingsReport)
			}

			readSnapshot := func() (store.CronJob, store.Setting, store.Setting, []store.AIOwnerMigration) {
				var job store.CronJob
				if err := gormDB.Where("id = ?", "job-coexist").First(&job).Error; err != nil {
					t.Fatal(err)
				}
				var source store.Setting
				if err := gormDB.Where(&store.Setting{Key: guard.PermissionSettingKey}).First(&source).Error; err != nil {
					t.Fatal(err)
				}
				var target store.Setting
				if err := gormDB.Where(&store.Setting{Key: guard.PermissionSettingKey + ".user.first-admin"}).First(&target).Error; err != nil {
					t.Fatal(err)
				}
				var ledgerRows []store.AIOwnerMigration
				if err := gormDB.Find(&ledgerRows).Error; err != nil {
					t.Fatal(err)
				}
				return job, source, target, ledgerRows
			}
			beforeJob, beforeSource, beforeTarget, beforeLedger := readSnapshot()
			if beforeJob.OwnerID != "owner-a" || beforeJob.OwnerKind != string(OwnerKindAccount) || !beforeJob.Enabled {
				t.Fatalf("coexisting cron row = %+v", beforeJob)
			}
			if beforeSource.Value != string(encoded) || beforeSource.UpdatedAt != 100 || beforeTarget.Value != beforeSource.Value || beforeTarget.UpdatedAt != 100 {
				t.Fatalf("settings rows changed unexpectedly: source=%+v target=%+v", beforeSource, beforeTarget)
			}
			if len(beforeLedger) != 2 {
				t.Fatalf("coexisting ledger rows = %d, want 2", len(beforeLedger))
			}
			ledgerByKey := func(rows []store.AIOwnerMigration) map[string]store.AIOwnerMigration {
				result := make(map[string]store.AIOwnerMigration, len(rows))
				for _, row := range rows {
					result[row.Migration+"/"+row.Kind+"/"+row.RecordID] = row
				}
				return result
			}
			beforeByKey := ledgerByKey(beforeLedger)
			if row := beforeByKey["conversation-owner-v1/cron_job/job-coexist"]; row.OwnerID != "owner-a" || row.AddedAt <= 0 {
				t.Fatalf("cron ledger = %+v", row)
			}
			if row := beforeByKey["global-ai-settings-owner-v1/permission/global"]; row.OwnerID != "first-admin" || row.AddedAt <= 0 {
				t.Fatalf("settings ledger = %+v", row)
			}

			if order == "cron-first" {
				cronReport, err = MigrateOwnerlessAI(ctx, gormDB, false)
				if err != nil {
					t.Fatal(err)
				}
				settingsReport, err = legacy.Migrate(ctx, gormDB, false)
			} else {
				settingsReport, err = legacy.Migrate(ctx, gormDB, false)
				if err != nil {
					t.Fatal(err)
				}
				cronReport, err = MigrateOwnerlessAI(ctx, gormDB, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if cronReport != (OwnerlessMigrationReport{}) || settingsReport != (legacy.Report{}) {
				t.Fatalf("reentry reports = cron %+v settings %+v", cronReport, settingsReport)
			}
			afterJob, afterSource, afterTarget, afterLedger := readSnapshot()
			if afterJob != beforeJob || afterSource != beforeSource || afterTarget != beforeTarget {
				t.Fatal("migration reentry changed business rows")
			}
			if len(afterLedger) != len(beforeLedger) {
				t.Fatalf("reentry ledger count = %d, want %d", len(afterLedger), len(beforeLedger))
			}
			afterByKey := ledgerByKey(afterLedger)
			for key, before := range beforeByKey {
				if afterByKey[key] != before {
					t.Fatalf("reentry changed ledger %s: before=%+v after=%+v", key, before, afterByKey[key])
				}
			}
		})
	}
}

func TestOwnerlessMigrationBlocksDueClaimDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM().WithContext(ctx)
	createLegacyMigrationRow(t, gormDB, &store.ConversationRow{ID: "conv-due", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1})
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 1, 0, 0, time.UTC))
	job := legacyMigrationCronJob("job-due", "conv-due")
	job.NextRunAt = clock.Now().Add(-time.Minute).UnixMilli()
	createLegacyMigrationRow(t, gormDB, &job)
	storage, err := NewSQLStore(ctx, gormDB, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	due, err := storage.ListDue(WithInternalAccess(ctx), clock.Now())
	if err != nil || len(due) != 1 || due[0].ID != job.ID {
		t.Fatalf("fixture was not due before migration: %+v, %v", due, err)
	}
	claimed := make(chan Trigger, 1)
	release := make(chan struct{})
	defer close(release)
	scheduler := testScheduler(t, storage, executorFunc(func(_ context.Context, trigger Trigger) error {
		claimed <- trigger
		<-release
		return nil
	}), clock)
	report, err := MigrateOwnerlessAI(ctx, gormDB, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.CronJobsQuarantined != 1 {
		t.Fatalf("migration report = %+v", report)
	}
	claimCount, err := scheduler.runOnce(ctx, clock.Now())
	if err != nil || claimCount != 0 {
		t.Fatalf("claim after migration = %d, %v", claimCount, err)
	}
	select {
	case trigger := <-claimed:
		t.Fatalf("migrated ownerless job was claimed: %+v", trigger)
	default:
	}
	var stored store.CronJob
	if err := gormDB.Where("id = ?", job.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled || stored.RunID != "" || stored.LeaseOwner != "" {
		t.Fatalf("migrated due job retained claimable state: %+v", stored)
	}
}

func TestOwnerlessAIMigrationSharedModeDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM().WithContext(ctx)
	createLegacyMigrationRow(t, gormDB, &store.ConversationRow{ID: "conv-owned", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: "owner-a"})
	createLegacyMigrationRow(t, gormDB, &store.ConversationRow{ID: "conv-orphan", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1})
	openJob := legacyMigrationCronJob("open-owned", "conv-owned")
	openJob.OwnerKind = string(OwnerKindOpen)
	legacyJob := legacyMigrationCronJob("legacy-orphan", "conv-orphan")
	createLegacyMigrationRow(t, gormDB, &openJob)
	createLegacyMigrationRow(t, gormDB, &legacyJob)

	report, err := MigrateOwnerlessAI(ctx, gormDB, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Changed() || report.Unattributed != 3 {
		t.Fatalf("shared migration report = %+v", report)
	}
	for _, id := range []string{openJob.ID, legacyJob.ID} {
		var stored store.CronJob
		if err := gormDB.Where("id = ?", id).First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if !stored.Enabled || stored.OwnerID != "" || stored.OwnerKind == string(OwnerKindAccount) {
			t.Fatalf("shared cron changed: %+v", stored)
		}
	}
	var markers int64
	if err := gormDB.Model(&store.AIOwnerMigration{}).Count(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatalf("shared migration created %d ledger rows", markers)
	}
}

func TestCronOwnerlessQueryIsolationDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM().WithContext(ctx)
	for _, row := range []store.CronJob{
		func() store.CronJob {
			job := legacyMigrationCronJob("job-a", "session")
			job.OwnerID = "owner-a"
			job.OwnerKind = string(OwnerKindAccount)
			return job
		}(),
		func() store.CronJob {
			job := legacyMigrationCronJob("job-b", "session")
			job.OwnerID = "owner-b"
			job.OwnerKind = string(OwnerKindAccount)
			return job
		}(),
		func() store.CronJob {
			job := legacyMigrationCronJob("job-admin", "session")
			job.OwnerID = "admin"
			job.OwnerKind = string(OwnerKindAccount)
			return job
		}(),
		legacyMigrationCronJob("job-legacy", "session"),
		func() store.CronJob {
			job := legacyMigrationCronJob("job-open", "session")
			job.OwnerKind = string(OwnerKindOpen)
			return job
		}(),
	} {
		createLegacyMigrationRow(t, gormDB, &row)
	}
	storage, err := NewSQLStore(ctx, gormDB, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
		want map[string]bool
	}{
		{name: "regular", ctx: ipc.WithUserID(ctx, "owner-a"), want: map[string]bool{"job-a": true}},
		{name: "superadmin", ctx: ipc.WithRole(ipc.WithUserID(ctx, "admin"), "superadmin"), want: map[string]bool{"job-admin": true, "job-legacy": true, "job-open": true}},
		{name: "internal", ctx: WithInternalAccess(ctx), want: map[string]bool{"job-a": true, "job-b": true, "job-admin": true, "job-legacy": true, "job-open": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobs, err := storage.List(test.ctx)
			if err != nil {
				t.Fatal(err)
			}
			requireCronJobIDs(t, jobs, test.want)
			due, err := storage.ListDue(test.ctx, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			requireCronJobIDs(t, due, test.want)
		})
	}
	if _, err := storage.List(context.Background()); err == nil {
		t.Fatal("anonymous account-mode List succeeded")
	}
	if _, err := storage.ListDue(context.Background(), time.Now()); err == nil {
		t.Fatal("anonymous account-mode ListDue succeeded")
	}
}

func requireCronJobIDs(t *testing.T, jobs []Job, want map[string]bool) {
	t.Helper()
	if len(jobs) != len(want) {
		t.Fatalf("job count = %d, want %d: %+v", len(jobs), len(want), jobs)
	}
	for _, job := range jobs {
		if !want[job.ID] {
			t.Fatalf("unexpected job %s in %+v", job.ID, jobs)
		}
	}
}
