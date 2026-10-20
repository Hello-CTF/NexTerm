package cron

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

type cronLedgerEntryKey struct {
	kind     string
	recordID string
}

var cronFaultExpectedReport = OwnerlessMigrationReport{
	CronJobsAssigned: 3, CronJobsQuarantined: 5, RunsAssigned: 2, MessagesAssigned: 1,
	RunEventsAssigned: 3, HitlRunsAssigned: 1, CheckpointsAssigned: 1, Unattributed: 17,
}

func seedCronMigrationFault(t *testing.T, database *gorm.DB) {
	t.Helper()
	for _, row := range []store.ConversationRow{
		legacyMigrationConversation("conv-a", "owner-a"),
		legacyMigrationConversation("conv-b", "owner-b"),
		legacyMigrationConversation("conv-orphan", ""),
	} {
		createLegacyMigrationRow(t, database, &row)
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
		createLegacyMigrationRow(t, database, &row)
	}

	for _, row := range []store.RunRow{
		legacyMigrationRun("run-a", "conv-a", ""),
		legacyMigrationRun("run-b", "conv-b", ""),
		legacyMigrationRun("run-owned-b", "conv-a", "owner-b"),
		legacyMigrationRun("run-orphan", "conv-orphan", ""),
		legacyMigrationRun("run-tombstoned", "conv-a", ""),
	} {
		createLegacyMigrationRow(t, database, &row)
	}
	for _, row := range []store.MessageRow{
		{ID: "message-a", ConversationID: "conv-a", Role: "user", ContentJSON: "{}", CreatedAt: 40, Seq: 1},
		{ID: "message-owned-b", ConversationID: "conv-a", Role: "user", ContentJSON: "{}", CreatedAt: 41, Seq: 2, OwnerID: "owner-b"},
		{ID: "message-orphan", ConversationID: "conv-orphan", Role: "user", ContentJSON: "{}", CreatedAt: 42, Seq: 1},
		{ID: "message-tombstoned", ConversationID: "conv-a", Role: "user", ContentJSON: "{}", CreatedAt: 43, Seq: 3},
	} {
		createLegacyMigrationRow(t, database, &row)
	}
	for _, row := range []store.RunEventRow{
		{RunID: "run-a", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 50},
		{RunID: "run-a", Seq: 2, Type: "delta", PayloadJSON: "{}", CreatedAt: 51},
		{RunID: "run-owned-b", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 52},
		{RunID: "run-orphan", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 53},
		{RunID: "run-tombstoned", Seq: 1, Type: "delta", PayloadJSON: "{}", CreatedAt: 54},
	} {
		createLegacyMigrationRow(t, database, &row)
	}
	for _, row := range []store.HitlRunRow{
		{ID: "run-a", Data: []byte("{}"), UpdatedAt: 60},
		{ID: "run-orphan", Data: []byte("{}"), UpdatedAt: 61},
		{ID: "run-tombstoned", Data: []byte("{}"), UpdatedAt: 62},
	} {
		createLegacyMigrationRow(t, database, &row)
	}
	for _, row := range []store.CheckpointRow{
		{ID: "run-a", Data: []byte("{}"), UpdatedAt: 70},
		{ID: "run-orphan", Data: []byte("{}"), UpdatedAt: 71},
		{ID: "takeover:recovery:job", Data: []byte("{}"), UpdatedAt: 72},
		{ID: "run-tombstoned", Data: []byte("{}"), UpdatedAt: 73},
	} {
		createLegacyMigrationRow(t, database, &row)
	}
	for _, row := range []store.SyncTombstone{
		{ID: "job-tombstoned", Kind: "cron_job", DeletedAt: 123, OwnerID: "owner-a"},
		{ID: "run-tombstoned", Kind: "ai_run", DeletedAt: 123, OwnerID: "owner-a"},
		{ID: "message-tombstoned", Kind: "ai_message", DeletedAt: 123, OwnerID: "owner-a"},
	} {
		createLegacyMigrationRow(t, database, &row)
	}
}

func cronFaultExpectedLedger() map[cronLedgerEntryKey]string {
	return map[cronLedgerEntryKey]string{
		{kind: "cron_job", recordID: "job-a"}:                          "owner-a",
		{kind: "cron_job", recordID: "job-disabled-owned"}:             "owner-a",
		{kind: "cron_job", recordID: "job-open-owned"}:                 "owner-a",
		{kind: "cron_job_quarantine", recordID: "job-open-orphan"}:     "",
		{kind: "cron_job_quarantine", recordID: "job-orphan"}:          "",
		{kind: "cron_job_quarantine", recordID: "job-missing"}:         "",
		{kind: "cron_job_quarantine", recordID: "job-disabled-orphan"}: "",
		{kind: "cron_job_quarantine", recordID: "job-tombstoned"}:      "",
		{kind: "ai_run", recordID: "run-a"}:                            "owner-a",
		{kind: "ai_run", recordID: "run-b"}:                            "owner-b",
		{kind: "ai_message", recordID: "message-a"}:                    "owner-a",
		{kind: "ai_run_event", recordID: "run-a:1"}:                    "owner-a",
		{kind: "ai_run_event", recordID: "run-a:2"}:                    "owner-a",
		{kind: "ai_run_event", recordID: "run-owned-b:1"}:              "owner-b",
		{kind: "ai_hitl_run", recordID: "run-a"}:                       "owner-a",
		{kind: "ai_checkpoint", recordID: "run-a"}:                     "owner-a",
	}
}

func verifyCronMigrationOutcome(t *testing.T, database *gorm.DB, report OwnerlessMigrationReport) {
	t.Helper()
	if report != cronFaultExpectedReport {
		t.Fatalf("migration report = %+v, want %+v", report, cronFaultExpectedReport)
	}
	for _, check := range []struct {
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
		requireLegacyMigrationOwner(t, database, check.model, check.id, check.ownerID)
	}
	for _, row := range []store.RunEventRow{
		{RunID: "run-a", Seq: 1}, {RunID: "run-a", Seq: 2}, {RunID: "run-owned-b", Seq: 1}, {RunID: "run-orphan", Seq: 1}, {RunID: "run-tombstoned", Seq: 1},
	} {
		var stored store.RunEventRow
		if err := database.Where("run_id = ? AND seq = ?", row.RunID, row.Seq).First(&stored).Error; err != nil {
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
		if err := database.Where("id = ?", id).First(&stored).Error; err != nil {
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
	if err := database.Where("id = ?", "job-owned-b").First(&preserved).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.OwnerID != "owner-b" || preserved.UpdatedAt != 99 {
		t.Fatalf("existing per-owner cron changed: %+v", preserved)
	}
	requireLegacyMigrationOwner(t, database, &store.ConversationRow{}, "conv-orphan", "")
	var tombstone store.SyncTombstone
	if err := database.Where("id = ?", "run-tombstoned").First(&tombstone).Error; err != nil {
		t.Fatal(err)
	}
	if tombstone.DeletedAt != 123 || tombstone.OwnerID != "owner-a" {
		t.Fatalf("tombstone changed: %+v", tombstone)
	}

	var ledger []store.AIOwnerMigration
	if err := database.Find(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	expectedLedger := cronFaultExpectedLedger()
	if len(ledger) != len(expectedLedger) {
		t.Fatalf("ledger rows = %d, want %d", len(ledger), len(expectedLedger))
	}
	seen := make(map[cronLedgerEntryKey]string, len(ledger))
	for _, row := range ledger {
		if row.Migration != ownerMigrationVersion || row.AddedAt <= 0 {
			t.Fatalf("invalid ledger marker: %+v", row)
		}
		seen[cronLedgerEntryKey{kind: row.Kind, recordID: row.RecordID}] = row.OwnerID
	}
	for key, wantOwner := range expectedLedger {
		ownerID, ok := seen[key]
		if !ok {
			t.Fatalf("ledger entry %+v is missing", key)
		}
		if ownerID != wantOwner {
			t.Fatalf("ledger entry %+v owner = %q, want %q", key, ownerID, wantOwner)
		}
	}
}

type cronMigrationSnapshot struct {
	conversations []store.ConversationRow
	cronJobs      []store.CronJob
	runs          []store.RunRow
	messages      []store.MessageRow
	runEvents     []store.RunEventRow
	hitlRuns      []store.HitlRunRow
	checkpoints   []store.CheckpointRow
	tombstones    []store.SyncTombstone
	ledger        []store.AIOwnerMigration
}

func snapshotCronMigration(t *testing.T, database *gorm.DB) cronMigrationSnapshot {
	t.Helper()
	var snapshot cronMigrationSnapshot
	if err := database.Order("id").Find(&snapshot.conversations).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("id").Find(&snapshot.cronJobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("id").Find(&snapshot.runs).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("id").Find(&snapshot.messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("run_id").Order("seq").Find(&snapshot.runEvents).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("id").Find(&snapshot.hitlRuns).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("id").Find(&snapshot.checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("owner_id").Order("id").Find(&snapshot.tombstones).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Order("migration").Order("kind").Order("record_id").Find(&snapshot.ledger).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func zeroCronLedgerAddedAt(snapshot cronMigrationSnapshot) cronMigrationSnapshot {
	normalized := snapshot
	normalized.conversations = append([]store.ConversationRow(nil), snapshot.conversations...)
	normalized.cronJobs = append([]store.CronJob(nil), snapshot.cronJobs...)
	normalized.runs = append([]store.RunRow(nil), snapshot.runs...)
	normalized.messages = append([]store.MessageRow(nil), snapshot.messages...)
	normalized.runEvents = append([]store.RunEventRow(nil), snapshot.runEvents...)
	normalized.hitlRuns = append([]store.HitlRunRow(nil), snapshot.hitlRuns...)
	normalized.checkpoints = append([]store.CheckpointRow(nil), snapshot.checkpoints...)
	normalized.tombstones = append([]store.SyncTombstone(nil), snapshot.tombstones...)
	normalized.ledger = append([]store.AIOwnerMigration(nil), snapshot.ledger...)
	for index := range normalized.ledger {
		normalized.ledger[index].AddedAt = 0
	}
	return normalized
}

func cronSnapshotRowsDiffer(got, want any, label string) string {
	gotRows := reflect.ValueOf(got)
	wantRows := reflect.ValueOf(want)
	if gotRows.Len() != wantRows.Len() {
		return fmt.Sprintf("%s count = %d, want %d\ngot: %+v\nwant: %+v", label, gotRows.Len(), wantRows.Len(), got, want)
	}
	for index := 0; index < gotRows.Len(); index++ {
		if !reflect.DeepEqual(gotRows.Index(index).Interface(), wantRows.Index(index).Interface()) {
			return fmt.Sprintf("%s row %d = %+v, want %+v", label, index, gotRows.Index(index).Interface(), wantRows.Index(index).Interface())
		}
	}
	return ""
}

func cronSnapshotDifference(got, want cronMigrationSnapshot) string {
	for _, table := range []struct {
		label     string
		got, want any
	}{
		{"conversations", got.conversations, want.conversations},
		{"cron jobs", got.cronJobs, want.cronJobs},
		{"runs", got.runs, want.runs},
		{"messages", got.messages, want.messages},
		{"run events", got.runEvents, want.runEvents},
		{"hitl runs", got.hitlRuns, want.hitlRuns},
		{"checkpoints", got.checkpoints, want.checkpoints},
		{"tombstones", got.tombstones, want.tombstones},
		{"ledger", got.ledger, want.ledger},
	} {
		if difference := cronSnapshotRowsDiffer(table.got, table.want, table.label); difference != "" {
			return difference
		}
	}
	return "snapshots differ"
}

// armCronMigrationWriteFault registers a create or update callback that trips
// on the hits-th write to the given table. The returned disarm func removes
// the callback and is safe to call more than once.
func armCronMigrationWriteFault(t *testing.T, database *gorm.DB, verb, table string, hits int, match func(any) bool, trip func(*gorm.DB)) (disarm func()) {
	t.Helper()
	remaining := hits
	callbackName := "nexterm_test:cron_owner_migration_" + verb + "_fault"
	callback := func(tx *gorm.DB) {
		if remaining <= 0 || tx.Statement.Table != table {
			return
		}
		if match != nil && !match(cronStatementDestValue(tx.Statement.Dest)) {
			return
		}
		remaining--
		if remaining == 0 {
			trip(tx)
		}
	}
	var err error
	switch verb {
	case "create":
		err = database.Callback().Create().Before("gorm:create").Register(callbackName, callback)
	case "update":
		err = database.Callback().Update().Before("gorm:update").Register(callbackName, callback)
	default:
		t.Fatalf("unknown fault verb %q", verb)
	}
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	disarm = func() {
		once.Do(func() {
			switch verb {
			case "create":
				_ = database.Callback().Create().Remove(callbackName)
			case "update":
				_ = database.Callback().Update().Remove(callbackName)
			}
		})
	}
	t.Cleanup(disarm)
	return disarm
}

func cronStatementDestValue(dest any) any {
	value := reflect.ValueOf(dest)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		return value.Elem().Interface()
	}
	return dest
}

func cronFaultLedgerRecord(kind, recordID string) func(any) bool {
	return func(value any) bool {
		row, ok := value.(store.AIOwnerMigration)
		return ok && row.Kind == kind && row.RecordID == recordID
	}
}

// cronFaultUpdateWithColumn matches an UpdateColumns assignment map that sets
// the given column, distinguishing cron job assignment from quarantine writes
// independently of the backend-dependent row scan order.
func cronFaultUpdateWithColumn(column string) func(any) bool {
	return func(value any) bool {
		assignments, ok := value.(map[string]any)
		if !ok {
			return false
		}
		_, present := assignments[column]
		return present
	}
}

// TestOwnerlessAIMigrationWriteFaultDatabase injects direct write faults into
// every cron legacy migration stage and verifies that a mid-migration failure
// rolls the whole transaction back and that a rerun after the crash converges
// to the same conversation, cron job, run, message, run event, hitl run,
// checkpoint, tombstone, and ledger state as an uninterrupted migration.
func TestOwnerlessAIMigrationWriteFaultDatabase(t *testing.T) {
	baselineStore := dbtest.NewFixture(t).OpenStore(t)
	seedCronMigrationFault(t, baselineStore.GORM())
	baselineReport, err := MigrateOwnerlessAI(context.Background(), baselineStore.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	verifyCronMigrationOutcome(t, baselineStore.GORM(), baselineReport)
	baseline := snapshotCronMigration(t, baselineStore.GORM())

	scenarios := []struct {
		name  string
		verb  string
		table string
		hits  int
		match func(any) bool
		crash bool
	}{
		{"CronJobAssign", "update", "cron_job", 1, cronFaultUpdateWithColumn("owner_id"), false},
		{"CronJobQuarantine", "update", "cron_job", 1, cronFaultUpdateWithColumn("enabled"), false},
		{"CronJobLedgerClaim", "create", "ai_owner_migration", 1, cronFaultLedgerRecord("cron_job", "job-a"), false},
		{"RunUpdate", "update", "ai_run", 1, nil, false},
		{"MessageLedgerClaim", "create", "ai_owner_migration", 1, cronFaultLedgerRecord("ai_message", "message-a"), false},
		{"RunEventUpdate", "update", "ai_run_event", 1, nil, false},
		{"HitlRunLedgerClaim", "create", "ai_owner_migration", 1, cronFaultLedgerRecord("ai_hitl_run", "run-a"), false},
		{"CheckpointUpdate", "update", "ai_checkpoint", 1, nil, false},
		{"CrashContextCancel", "update", "ai_run_event", 1, nil, true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			database := dbtest.NewFixture(t).OpenStore(t)
			seedCronMigrationFault(t, database.GORM())
			before := snapshotCronMigration(t, database.GORM())

			ctx := context.Background()
			trip := func(tx *gorm.DB) { tx.AddError(errors.New("forced cron owner migration write failure")) }
			if scenario.crash {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				trip = func(*gorm.DB) { cancel() }
			}
			disarm := armCronMigrationWriteFault(t, database.GORM().WithContext(ctx), scenario.verb, scenario.table, scenario.hits, scenario.match, trip)

			report, err := MigrateOwnerlessAI(ctx, database.GORM().WithContext(ctx), false)
			disarm()
			if err == nil {
				t.Fatal("faulted migration succeeded")
			}
			if report != (OwnerlessMigrationReport{}) {
				t.Fatalf("faulted migration report = %+v, want zero", report)
			}
			if after := snapshotCronMigration(t, database.GORM()); !reflect.DeepEqual(before, after) {
				t.Fatalf("faulted migration was not fully rolled back: %s", cronSnapshotDifference(after, before))
			}

			rerun, err := MigrateOwnerlessAI(context.Background(), database.GORM(), false)
			if err != nil {
				t.Fatalf("crash rerun failed: %v", err)
			}
			verifyCronMigrationOutcome(t, database.GORM(), rerun)
			converged := snapshotCronMigration(t, database.GORM())
			if after := zeroCronLedgerAddedAt(converged); !reflect.DeepEqual(after, zeroCronLedgerAddedAt(baseline)) {
				t.Fatalf("crash rerun did not converge to the uninterrupted baseline: %s", cronSnapshotDifference(after, zeroCronLedgerAddedAt(baseline)))
			}

			repeated, err := MigrateOwnerlessAI(context.Background(), database.GORM(), false)
			if err != nil {
				t.Fatalf("idempotent reentry failed: %v", err)
			}
			if repeated != (OwnerlessMigrationReport{Unattributed: 17}) {
				t.Fatalf("idempotent reentry report = %+v", repeated)
			}
			if after := snapshotCronMigration(t, database.GORM()); !reflect.DeepEqual(after, converged) {
				t.Fatalf("idempotent reentry changed database rows: %s", cronSnapshotDifference(after, converged))
			}
		})
	}
}

// TestOwnerlessAIMigrationLedgerAddedAtOracleDatabase proves the exact
// third-run comparison catches ledger added-at rewrites that the normalized
// baseline comparison intentionally ignores.
func TestOwnerlessAIMigrationLedgerAddedAtOracleDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	gormDB := database.GORM()
	createLegacyMigrationRow(t, gormDB, &store.ConversationRow{ID: "conv-oracle", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: "owner-a"})
	job := legacyMigrationCronJob("job-oracle", "conv-oracle")
	createLegacyMigrationRow(t, gormDB, &job)
	if _, err := MigrateOwnerlessAI(context.Background(), gormDB, false); err != nil {
		t.Fatal(err)
	}
	captured := snapshotCronMigration(t, gormDB)
	if err := gormDB.Model(&store.AIOwnerMigration{}).
		Where("migration = ? AND kind = ? AND record_id = ?", ownerMigrationVersion, "cron_job", "job-oracle").
		Update("added_at", 1).Error; err != nil {
		t.Fatal(err)
	}
	after := snapshotCronMigration(t, gormDB)
	if reflect.DeepEqual(after, captured) {
		t.Fatal("exact comparison missed a rewritten ledger added-at timestamp")
	}
	if difference := cronSnapshotDifference(after, captured); !strings.Contains(difference, "ledger row") {
		t.Fatalf("exact comparison difference = %q, want a ledger row mismatch", difference)
	}
	if !reflect.DeepEqual(zeroCronLedgerAddedAt(after), zeroCronLedgerAddedAt(captured)) {
		t.Fatal("normalized comparison must ignore added-at; the exact third-run comparison is what catches rewrites")
	}
}
