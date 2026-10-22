package cron

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

const (
	cronScanVolumeFuture    = 1900
	cronScanVolumeDue       = 80
	cronScanVolumeActive    = 15
	cronScanVolumeAbandoned = 5
)

func seedCronScanVolume(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	stamp := now.UnixMilli()
	past := stamp - 120000
	pastLease := stamp - 60000
	futureLease := stamp + 3600000
	futureDeadline := stamp + 3600000
	rows := make([]store.CronJob, 0, cronScanVolumeFuture+cronScanVolumeDue+cronScanVolumeActive+cronScanVolumeAbandoned)
	for index := 0; index < cronScanVolumeFuture; index++ {
		rows = append(rows, store.CronJob{
			ID: fmt.Sprintf("future-%05d", index), SessionID: "volume", Prompt: "volume", Schedule: "* * * * *", Timezone: "UTC",
			Enabled: true, TimeoutMS: 60000, CreatedAt: stamp, UpdatedAt: stamp, Revision: 1,
			NextRunAt: stamp + 3600000, OwnerKind: string(OwnerKindOpen),
		})
	}
	for index := 0; index < cronScanVolumeDue; index++ {
		rows = append(rows, store.CronJob{
			ID: fmt.Sprintf("due-%05d", index), SessionID: "volume", Prompt: "volume", Schedule: "* * * * *", Timezone: "UTC",
			Enabled: true, TimeoutMS: 60000, CreatedAt: stamp, UpdatedAt: stamp, Revision: 1,
			NextRunAt: stamp - 60000, OwnerKind: string(OwnerKindOpen),
		})
	}
	for index := 0; index < cronScanVolumeActive; index++ {
		runID := fmt.Sprintf("active-run-%02d", index)
		rows = append(rows, store.CronJob{
			ID: fmt.Sprintf("active-%02d", index), SessionID: "volume", Prompt: "volume", Schedule: "* * * * *", Timezone: "UTC",
			Enabled: true, TimeoutMS: 60000, CreatedAt: stamp, UpdatedAt: stamp, Revision: 1,
			NextRunAt: stamp + 60000, OwnerKind: string(OwnerKindOpen),
			RunID: runID, RunScheduledFor: &past, RunStartedAt: &past, RunDeadline: &futureDeadline,
			LeaseOwner: "live-owner", LeaseExpiresAt: &futureLease,
		})
	}
	for index := 0; index < cronScanVolumeAbandoned; index++ {
		runID := fmt.Sprintf("abandoned-run-%02d", index)
		rows = append(rows, store.CronJob{
			ID: fmt.Sprintf("abandoned-%02d", index), SessionID: "volume", Prompt: "volume", Schedule: "* * * * *", Timezone: "UTC",
			Enabled: true, TimeoutMS: 60000, CreatedAt: stamp, UpdatedAt: stamp, Revision: 1,
			NextRunAt: stamp + 60000, OwnerKind: string(OwnerKindOpen),
			RunID: runID, RunScheduledFor: &past, RunStartedAt: &past, RunDeadline: &past,
			LeaseOwner: "dead-owner", LeaseExpiresAt: &pastLease,
		})
	}
	if err := db.CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatal(err)
	}
	switch db.Dialector.Name() {
	case "postgres":
		if err := db.Exec("ANALYZE cron_job").Error; err != nil {
			t.Fatal(err)
		}
	case "mysql":
		if err := db.Exec("ANALYZE TABLE cron_job").Error; err != nil {
			t.Fatal(err)
		}
	}
}

func explainPlanText(t *testing.T, db *gorm.DB, backend store.Backend, query string) string {
	t.Helper()
	prefix := "EXPLAIN "
	if backend == store.BackendSQLite {
		prefix = "EXPLAIN QUERY PLAN "
	}
	rows, err := db.Raw(prefix + query).Rows()
	if err != nil {
		t.Fatalf("explain query: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for index, column := range columns {
			if text, ok := values[index].([]byte); ok {
				values[index] = string(text)
			}
			fmt.Fprintf(&plan, "%s=%v ", column, values[index])
		}
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

func cronScanExplainSQL(t *testing.T, now time.Time) (idle, recovery string) {
	t.Helper()
	stamp := fmt.Sprintf("%d", now.UnixMilli())
	idleWhere := strings.Replace(cronDueIdleWhere, "enabled = ?", "enabled = TRUE", 1)
	idleWhere = strings.ReplaceAll(idleWhere, "?", stamp)
	recoveryWhere := strings.ReplaceAll(cronRecoveryWhere, "?", stamp)
	idle = fmt.Sprintf("SELECT * FROM cron_job WHERE %s ORDER BY %s LIMIT 64", idleWhere, cronDueIdleOrder)
	recovery = fmt.Sprintf("SELECT * FROM cron_job WHERE %s ORDER BY %s LIMIT 64", recoveryWhere, cronRecoveryOrder)
	return idle, recovery
}

func TestDatabaseCronScanQueriesUseIndexes(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	backend := fixture.OpenStore(t)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	seedCronScanVolume(t, backend.GORM(), now)
	idleSQL, recoverySQL := cronScanExplainSQL(t, now)

	assertIndexed := func(name, plan, index string) {
		t.Helper()
		switch fixture.Backend {
		case store.BackendSQLite:
			if !strings.Contains(plan, "USING INDEX "+index) || strings.Contains(plan, "SCAN cron_job") {
				t.Fatalf("%s plan is not an index scan on %s:\n%s", name, index, plan)
			}
		case store.BackendPostgres:
			if !strings.Contains(plan, index) || strings.Contains(plan, "Seq Scan on cron_job") {
				t.Fatalf("%s plan is not an index scan on %s:\n%s", name, index, plan)
			}
		case store.BackendMySQL, store.BackendMariaDB:
			if !strings.Contains(plan, " key="+index) || strings.Contains(plan, "type=ALL") {
				t.Fatalf("%s plan is not an index scan on %s:\n%s", name, index, plan)
			}
		}
	}
	assertIndexed("idle due scan", explainPlanText(t, backend.GORM(), fixture.Backend, idleSQL), "idx_cron_job_due")
	assertIndexed("recovery scan", explainPlanText(t, backend.GORM(), fixture.Backend, recoverySQL), "idx_cron_job_recovery")
}

func TestDatabaseCronScanVolumeDrainsWithoutLoss(t *testing.T) {
	ctx := context.Background()
	fixture := dbtest.NewFixture(t)
	backend := fixture.OpenStore(t)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	seedCronScanVolume(t, backend.GORM(), now)
	cronStore, err := NewSQLStore(ctx, backend.GORM(), WithShared(true))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &scanRecorder{}
	store := &recordingStore{Store: cronStore, recorder: recorder}
	clock := newFakeClock(now)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	executed := make(chan string, cronScanVolumeDue)
	executor := executorFunc(func(_ context.Context, trigger Trigger) error {
		executed <- trigger.JobID
		<-release
		return nil
	})
	scheduler := testScheduler(t, store, executor, clock, func(options *Options) {
		options.MaxConcurrentExecutions = 4
	})

	claimed, err := scheduler.runOnce(ctx, now)
	if err != nil || claimed != 4 {
		t.Fatalf("first runOnce = %d, %v; want 4 with free slots", claimed, err)
	}
	idleScanCount := len(recorder.idleSnapshot())
	claimed, err = scheduler.runOnce(ctx, now)
	if err != nil || claimed != 0 {
		t.Fatalf("full-slot runOnce = %d, %v; want 0", claimed, err)
	}
	if got := len(recorder.idleSnapshot()); got != idleScanCount {
		t.Fatalf("idle scans after full-slot tick = %d; want %d (no prefetch)", got, idleScanCount)
	}
	releaseOnce.Do(func() { close(release) })
	waitSchedulerWithTimeout(t, scheduler)

	for attempts := 0; len(executed) < cronScanVolumeDue && attempts < cronScanVolumeDue/2+10; attempts++ {
		if _, err := scheduler.runOnce(ctx, now); err != nil {
			t.Fatal(err)
		}
		waitSchedulerWithTimeout(t, scheduler)
	}
	waitSchedulerWithTimeout(t, scheduler)

	if got := len(executed); got != cronScanVolumeDue {
		t.Fatalf("executions = %d; want %d (no loss)", got, cronScanVolumeDue)
	}
	unique := make(map[string]bool, cronScanVolumeDue)
	for len(executed) > 0 {
		unique[<-executed] = true
	}
	if len(unique) != cronScanVolumeDue {
		t.Fatalf("unique executions = %d; want %d (no duplicates)", len(unique), cronScanVolumeDue)
	}
	for index := 0; index < cronScanVolumeAbandoned; index++ {
		job := loadStoredJob(t, cronStore, fmt.Sprintf("abandoned-%02d", index))
		if job.Running() || job.ConsecutiveFailures != 1 || !strings.Contains(job.LastError, "interrupted") {
			t.Fatalf("abandoned run %s not reconciled: %+v", job.ID, job)
		}
	}
	for index := 0; index < cronScanVolumeActive; index++ {
		job := loadStoredJob(t, cronStore, fmt.Sprintf("active-%02d", index))
		if !job.Running() || job.Run.ID != fmt.Sprintf("active-run-%02d", index) {
			t.Fatalf("active run %s was reconciled: %+v", job.ID, job)
		}
	}
	idleScans := recorder.idleSnapshot()
	if len(idleScans) < cronScanVolumeDue/4 {
		t.Fatalf("idle scans = %d; want batched draining", len(idleScans))
	}
	for _, scan := range idleScans {
		if scan.limit > 4 || scan.rows > 4 {
			t.Fatalf("idle scan prefetched beyond free slots: %+v", scan)
		}
	}
	recoveryScans := recorder.recoverySnapshot()
	for _, scan := range recoveryScans {
		if scan.limit > DefaultRecoveryBatchSize || scan.rows > DefaultRecoveryBatchSize {
			t.Fatalf("recovery scan exceeded batch size: %+v", scan)
		}
	}
}
