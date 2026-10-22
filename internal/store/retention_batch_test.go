package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

type retentionTransactionSample struct {
	duration   time.Duration
	beginWait  time.Duration
	statements int
	deleted    int64
	committed  bool
}

type retentionTransactionRecorder struct {
	mu        sync.Mutex
	delay     time.Duration
	active    int
	maxActive int
	samples   []retentionTransactionSample
}

func (r *retentionTransactionRecorder) install(db *Store) {
	pool := &retentionMeasuringConnPool{ConnPool: db.db.Statement.ConnPool, recorder: r}
	db.db.Config.ConnPool = pool
	db.db.Statement.ConnPool = pool
}

func (r *retentionTransactionRecorder) begin() {
	r.mu.Lock()
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	r.mu.Unlock()
}

func (r *retentionTransactionRecorder) finish(sample retentionTransactionSample) {
	r.mu.Lock()
	r.active--
	r.samples = append(r.samples, sample)
	r.mu.Unlock()
}

func (r *retentionTransactionRecorder) snapshot() ([]retentionTransactionSample, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]retentionTransactionSample(nil), r.samples...), r.maxActive
}

type retentionMeasuringConnPool struct {
	gorm.ConnPool
	recorder *retentionTransactionRecorder
}

func (p *retentionMeasuringConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	beginner, ok := p.ConnPool.(gorm.TxBeginner)
	if !ok {
		return nil, fmt.Errorf("retention test connection %T cannot begin transactions", p.ConnPool)
	}
	started := time.Now()
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	p.recorder.begin()
	return &retentionMeasuringTx{Tx: tx, recorder: p.recorder, started: started, beginWait: time.Since(started)}, nil
}

type retentionMeasuringTx struct {
	*sql.Tx
	recorder   *retentionTransactionRecorder
	started    time.Time
	beginWait  time.Duration
	statements int
	deleted    int64
}

func (tx *retentionMeasuringTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx.recorder.delay > 0 && strings.HasPrefix(strings.TrimSpace(strings.ToUpper(query)), "DELETE") {
		time.Sleep(tx.recorder.delay)
	}
	result, err := tx.Tx.ExecContext(ctx, query, args...)
	if err == nil && strings.HasPrefix(strings.TrimSpace(strings.ToUpper(query)), "DELETE") {
		tx.statements++
		if affected, affectedErr := result.RowsAffected(); affectedErr == nil {
			tx.deleted += affected
		}
	}
	return result, err
}

func (tx *retentionMeasuringTx) Commit() error {
	err := tx.Tx.Commit()
	tx.recorder.finish(retentionTransactionSample{
		duration: time.Since(tx.started), beginWait: tx.beginWait, statements: tx.statements,
		deleted: tx.deleted, committed: err == nil,
	})
	return err
}

func (tx *retentionMeasuringTx) Rollback() error {
	err := tx.Tx.Rollback()
	tx.recorder.finish(retentionTransactionSample{
		duration: time.Since(tx.started), beginWait: tx.beginWait, statements: tx.statements,
		deleted: tx.deleted, committed: false,
	})
	return err
}

func TestRetentionLargeHistoryUsesBoundedTransactions(t *testing.T) {
	ctx := context.Background()
	db, _ := fileStore(t)
	const batchSize = 128
	const expiredRows = 3*batchSize + 17
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	recent := time.Now().UnixMilli()
	for i := 0; i < expiredRows; i++ {
		insertAuditAt(t, db, old, "expired")
		endedAt := old
		insertRecordingAt(t, db, fmt.Sprintf("expired-%d", i), "external.cast", old, &endedAt)
		if _, err := db.DB().Exec(`INSERT INTO command_log(session_id, tab_id, asset_id, command, source, started_at, finished_at)
		VALUES('session', 'tab', 'asset', 'retention-fixture', 'test', ?, ?)`, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		insertAuditAt(t, db, recent, "recent")
	}
	insertRecordingAt(t, db, "active", "active.cast", old, nil)

	recorder := &retentionTransactionRecorder{delay: 2 * time.Millisecond}
	recorder.install(db)
	started := time.Now()
	result, err := db.EnforceRetention(ctx, RetentionPolicy{
		AuditMaxAge: time.Hour, AuditMaxCount: 8,
		RecordingMaxAge: time.Hour, RecordingMaxCount: 8,
		CommandMaxAge: time.Hour, CommandMaxCount: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AuditDeleted != expiredRows || result.RecordingsDeleted != expiredRows || result.CommandsDeleted != expiredRows {
		t.Fatalf("retention result = %+v, want %d expired rows per table", result, expiredRows)
	}
	samples, maxActive := recorder.snapshot()
	var maxDeleted int64
	var maxTransaction, totalTransaction, maxBeginWait time.Duration
	for _, sample := range samples {
		if sample.deleted > maxDeleted {
			maxDeleted = sample.deleted
		}
		if sample.duration > maxTransaction {
			maxTransaction = sample.duration
		}
		if sample.beginWait > maxBeginWait {
			maxBeginWait = sample.beginWait
		}
		totalTransaction += sample.duration
	}
	t.Logf("retention metrics wall=%s transactions=%d maxDeletedPerTx=%d totalTx=%s maxTx=%s maxBeginWait=%s maxActive=%d",
		time.Since(started), len(samples), maxDeleted, totalTransaction, maxTransaction, maxBeginWait, maxActive)
	if len(samples) <= 1 {
		t.Fatalf("retention used %d transaction for %d rows per table", len(samples), expiredRows)
	}
	if maxDeleted > batchSize {
		t.Fatalf("largest transaction deleted %d rows, want <= %d", maxDeleted, batchSize)
	}
	if maxTransaction >= totalTransaction {
		t.Fatalf("one transaction covers the complete cleanup: max=%s total=%s", maxTransaction, totalTransaction)
	}
	if maxActive != 1 {
		t.Fatalf("maximum active transactions = %d, want 1", maxActive)
	}
	requireTableCount(t, db, "audit_log", 8)
	requireTableCount(t, db, "terminal_recording", 1)
	requireTableCount(t, db, "command_log", 0)
}

func TestRetentionResumesAfterFailedBatch(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	const total = 2*retentionBatchSize + 19
	for i := 0; i < total; i++ {
		kind := "expired"
		if i == retentionBatchSize {
			kind = "fail-batch"
		}
		insertAuditAt(t, db, old, kind)
	}
	if _, err := db.DB().Exec(`CREATE TRIGGER fail_second_retention_batch BEFORE DELETE ON audit_log
WHEN OLD.kind = 'fail-batch'
BEGIN SELECT RAISE(ABORT, 'injected second batch failure'); END`); err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{AuditMaxAge: time.Hour}
	if _, err := db.EnforceRetention(ctx, policy); err == nil {
		t.Fatal("expected second batch failure")
	}
	requireTableCount(t, db, "audit_log", total-retentionBatchSize)
	if !strings.Contains(db.RetentionStatus().LastError, "injected second batch failure") {
		t.Fatalf("retention status = %+v", db.RetentionStatus())
	}
	if _, err := db.DB().Exec("DROP TRIGGER fail_second_retention_batch"); err != nil {
		t.Fatal(err)
	}
	result, err := db.EnforceRetention(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.AuditDeleted != total-retentionBatchSize {
		t.Fatalf("resumed deletion = %d, want %d", result.AuditDeleted, total-retentionBatchSize)
	}
	requireTableCount(t, db, "audit_log", 0)
}

func TestRetentionScheduleStaggersAndSerializesStartup(t *testing.T) {
	db := testStore(t)
	scheduler := db.schedulerForRetention()
	scheduler.stagger = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	started := make(chan string, 3)
	var mu sync.Mutex
	active, maxActive := 0, 0
	var wg sync.WaitGroup
	defer func() {
		cancel()
		release()
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("retention schedules did not stop")
		}
	}()

	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("startup-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := db.RunRetentionSchedule(ctx, RetentionSchedule{
				Name: name, Interval: time.Hour,
				Run: func(context.Context) {
					mu.Lock()
					active++
					if active > maxActive {
						maxActive = active
					}
					mu.Unlock()
					started <- name
					select {
					case <-gate:
					case <-ctx.Done():
					}
					mu.Lock()
					active--
					mu.Unlock()
				},
			}); err != nil {
				t.Errorf("run retention schedule: %v", err)
			}
		}()
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first scheduled cleanup did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		scheduler.mu.Lock()
		registered := len(scheduler.jobs)
		scheduler.mu.Unlock()
		if registered == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registered schedules = %d, want 3", registered)
		}
		time.Sleep(time.Millisecond)
	}
	scheduler.mu.Lock()
	var firstDue, lastDue time.Time
	for job := range scheduler.jobs {
		if firstDue.IsZero() || job.due.Before(firstDue) {
			firstDue = job.due
		}
		if job.due.After(lastDue) {
			lastDue = job.due
		}
	}
	scheduler.mu.Unlock()
	if spread := lastDue.Sub(firstDue); spread < 60*time.Millisecond {
		t.Fatalf("startup due spread = %s, want at least 60ms", spread)
	}
	release()
	for i := 1; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("remaining scheduled cleanups did not start")
		}
	}
	mu.Lock()
	if maxActive != 1 {
		mu.Unlock()
		t.Fatalf("maximum concurrent cleanups = %d, want 1", maxActive)
	}
	mu.Unlock()
}
