package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
)

type RetentionPolicy struct {
	AuditMaxAge       time.Duration
	AuditMaxCount     int64
	RecordingMaxAge   time.Duration
	RecordingMaxCount int64
	AIRunMaxAge       time.Duration
	AIRunMaxCount     int64
	CommandMaxAge     time.Duration
	CommandMaxCount   int64
	ExecBatchMaxAge   time.Duration
	ExecBatchMaxCount int64
}

type RetentionResult struct {
	AuditDeleted       int64 `json:"auditDeleted"`
	RecordingsDeleted  int64 `json:"recordingsDeleted"`
	AIRunsDeleted      int64 `json:"aiRunsDeleted"`
	CommandsDeleted    int64 `json:"commandsDeleted"`
	ExecBatchesDeleted int64 `json:"execBatchesDeleted"`
}

type RetentionStatus struct {
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	LastSuccessAt time.Time `json:"lastSuccessAt"`
	LastError     string    `json:"lastError,omitempty"`
}

type retentionState struct {
	sync.RWMutex
	status    RetentionStatus
	scheduler *retentionScheduler
}

const (
	retentionBatchSize      = 128
	retentionStartupStagger = 2 * time.Second
)

type RetentionSchedule struct {
	Name     string
	Interval time.Duration
	Run      func(context.Context)
}

type retentionJob struct {
	ctx      context.Context
	name     string
	interval time.Duration
	run      func(context.Context)
	due      time.Time
	running  bool
	done     chan struct{}
}

type retentionScheduler struct {
	store   *Store
	mu      sync.Mutex
	jobs    map[*retentionJob]struct{}
	wake    chan struct{}
	running bool
	stagger time.Duration
}

func (s *Store) schedulerForRetention() *retentionScheduler {
	s.retention.Lock()
	defer s.retention.Unlock()
	if s.retention.scheduler == nil {
		s.retention.scheduler = &retentionScheduler{
			store: s, jobs: make(map[*retentionJob]struct{}), wake: make(chan struct{}, 1), stagger: retentionStartupStagger,
		}
	}
	return s.retention.scheduler
}

func (s *Store) RunRetentionSchedule(ctx context.Context, schedule RetentionSchedule) error {
	if ctx == nil {
		return fmt.Errorf("retention schedule context is required")
	}
	if schedule.Name == "" || schedule.Interval <= 0 || schedule.Run == nil {
		return fmt.Errorf("invalid retention schedule")
	}
	scheduler := s.schedulerForRetention()
	job := &retentionJob{
		ctx: ctx, name: schedule.Name, interval: schedule.Interval, run: schedule.Run, done: make(chan struct{}),
	}
	scheduler.add(job)
	<-ctx.Done()
	scheduler.remove(job)
	<-job.done
	return nil
}

func (s *retentionScheduler) add(job *retentionJob) {
	s.mu.Lock()
	job.due = time.Now().Add(time.Duration(len(s.jobs)) * s.stagger)
	s.jobs[job] = struct{}{}
	if !s.running {
		s.running = true
		go s.run()
	}
	s.mu.Unlock()
	s.signal()
}

func (s *retentionScheduler) remove(job *retentionJob) {
	s.mu.Lock()
	if _, ok := s.jobs[job]; ok {
		delete(s.jobs, job)
		if !job.running {
			close(job.done)
		}
	}
	s.mu.Unlock()
	s.signal()
}

func (s *retentionScheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *retentionScheduler) run() {
	for {
		s.mu.Lock()
		if len(s.jobs) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		now := time.Now()
		var next *retentionJob
		for job := range s.jobs {
			if job.ctx.Err() == nil && (next == nil || job.due.Before(next.due)) {
				next = job
			}
		}
		s.mu.Unlock()
		if next == nil {
			<-s.wake
			continue
		}
		if wait := next.due.Sub(now); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-s.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			continue
		}
		s.execute(next)
	}
}

func (s *retentionScheduler) execute(job *retentionJob) {
	s.mu.Lock()
	if _, ok := s.jobs[job]; !ok || job.running || job.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	job.running = true
	started := time.Now()
	wait := started.Sub(job.due)
	s.mu.Unlock()

	job.run(job.ctx)
	finished := time.Now()

	s.mu.Lock()
	job.running = false
	job.due = finished.Add(job.interval)
	if _, ok := s.jobs[job]; !ok {
		close(job.done)
	}
	s.mu.Unlock()
	if wait < 0 {
		wait = 0
	}
	s.store.logger.Info("scheduled retention cleanup finished",
		"job", job.name,
		"scheduler_wait_ms", retentionMilliseconds(wait),
		"duration_ms", retentionMilliseconds(finished.Sub(started)),
	)
}

func retentionMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func (p RetentionPolicy) validate() error {
	for name, value := range map[string]time.Duration{
		"audit max age":      p.AuditMaxAge,
		"recording max age":  p.RecordingMaxAge,
		"AI run max age":     p.AIRunMaxAge,
		"command max age":    p.CommandMaxAge,
		"exec batch max age": p.ExecBatchMaxAge,
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	for name, value := range map[string]int64{
		"audit max count":      p.AuditMaxCount,
		"recording max count":  p.RecordingMaxCount,
		"AI run max count":     p.AIRunMaxCount,
		"command max count":    p.CommandMaxCount,
		"exec batch max count": p.ExecBatchMaxCount,
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	return nil
}

func (p RetentionPolicy) empty() bool {
	return p.AuditMaxAge == 0 && p.AuditMaxCount == 0 &&
		p.RecordingMaxAge == 0 && p.RecordingMaxCount == 0 &&
		p.AIRunMaxAge == 0 && p.AIRunMaxCount == 0 &&
		p.CommandMaxAge == 0 && p.CommandMaxCount == 0 &&
		p.ExecBatchMaxAge == 0 && p.ExecBatchMaxCount == 0
}

func (s *Store) EnforceRetention(ctx context.Context, policy RetentionPolicy) (result RetentionResult, returnErr error) {
	defer func() { s.recordRetentionResult(returnErr) }()
	if err := policy.validate(); err != nil {
		return RetentionResult{}, badParam(err)
	}
	if policy.empty() {
		return RetentionResult{}, nil
	}
	if err := s.enforceRetentionBatches(ctx, policy, time.Now(), &result); err != nil {
		return RetentionResult{}, dbError(err)
	}
	return result, nil
}

func (s *Store) enforceRetentionBatches(ctx context.Context, policy RetentionPolicy, now time.Time, result *RetentionResult) error {
	if policy.ExecBatchMaxAge > 0 || policy.ExecBatchMaxCount > 0 {
		deleted, err := s.enforceExecBatchRetention(ctx, policy, now)
		result.ExecBatchesDeleted += deleted
		if err != nil {
			return err
		}
	}
	if policy.AuditMaxAge > 0 {
		cutoff := now.Add(-policy.AuditMaxAge).UnixMilli()
		deleted, err := retentionDeleteBatches(ctx, s, "audit_log", "age",
			func(tx *gorm.DB) ([]int64, error) {
				audit := query.Use(tx).AuditRow
				ids := make([]int64, 0, retentionBatchSize)
				err := audit.WithContext(ctx).Where(audit.TS.Lt(cutoff)).Order(audit.ID.Asc()).Limit(retentionBatchSize).Pluck(audit.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []int64) (int64, error) {
				audit := query.Use(tx).AuditRow
				info, err := audit.WithContext(ctx).Where(audit.ID.In(ids...), audit.TS.Lt(cutoff)).Delete()
				return info.RowsAffected, err
			},
		)
		result.AuditDeleted += deleted
		if err != nil {
			return fmt.Errorf("audit age retention: %w", err)
		}
	}
	if policy.AuditMaxCount > 0 {
		deleted, err := retentionDeleteBatches(ctx, s, "audit_log", "count",
			func(tx *gorm.DB) ([]int64, error) {
				audit := query.Use(tx).AuditRow
				ids := make([]int64, 0, retentionBatchSize)
				err := audit.WithContext(ctx).Order(audit.TS.Desc(), audit.ID.Desc()).Limit(retentionBatchSize).Offset(int(policy.AuditMaxCount)).Pluck(audit.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []int64) (int64, error) {
				audit := query.Use(tx).AuditRow
				info, err := audit.WithContext(ctx).Where(audit.ID.In(ids...)).Delete()
				return info.RowsAffected, err
			},
		)
		result.AuditDeleted += deleted
		if err != nil {
			return fmt.Errorf("audit count retention: %w", err)
		}
	}
	if policy.RecordingMaxAge > 0 {
		cutoff := now.Add(-policy.RecordingMaxAge).UnixMilli()
		deleted, err := retentionDeleteBatches(ctx, s, "terminal_recording", "age",
			func(tx *gorm.DB) ([]string, error) {
				recording := query.Use(tx).RecordingRow
				ids := make([]string, 0, retentionBatchSize)
				err := recording.WithContext(ctx).
					Where(recording.EndedAt.IsNotNull(), recording.EndedAt.Lt(cutoff)).
					Order(recording.ID.Asc()).Limit(retentionBatchSize).Pluck(recording.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []string) (int64, error) {
				recording := query.Use(tx).RecordingRow
				info, err := recording.WithContext(ctx).
					Where(recording.ID.In(ids...), recording.EndedAt.IsNotNull(), recording.EndedAt.Lt(cutoff)).Delete()
				return info.RowsAffected, err
			},
		)
		result.RecordingsDeleted += deleted
		if err != nil {
			return fmt.Errorf("recording age retention: %w", err)
		}
	}
	if policy.RecordingMaxCount > 0 {
		deleted, err := retentionDeleteBatches(ctx, s, "terminal_recording", "count",
			func(tx *gorm.DB) ([]string, error) {
				recording := query.Use(tx).RecordingRow
				ids := make([]string, 0, retentionBatchSize)
				err := recording.WithContext(ctx).
					Where(recording.EndedAt.IsNotNull()).
					Order(recording.EndedAt.Desc(), recording.ID.Desc()).
					Limit(retentionBatchSize).Offset(int(policy.RecordingMaxCount)).Pluck(recording.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []string) (int64, error) {
				recording := query.Use(tx).RecordingRow
				info, err := recording.WithContext(ctx).Where(recording.ID.In(ids...), recording.EndedAt.IsNotNull()).Delete()
				return info.RowsAffected, err
			},
		)
		result.RecordingsDeleted += deleted
		if err != nil {
			return fmt.Errorf("recording count retention: %w", err)
		}
	}
	if policy.CommandMaxAge > 0 {
		cutoff := now.Add(-policy.CommandMaxAge).UnixMilli()
		deleted, err := retentionDeleteBatches(ctx, s, "command_log", "age",
			func(tx *gorm.DB) ([]int64, error) {
				command := query.Use(tx).CommandLog
				ids := make([]int64, 0, retentionBatchSize)
				err := command.WithContext(ctx).Where(command.StartedAt.Lt(cutoff)).Order(command.ID.Asc()).Limit(retentionBatchSize).Pluck(command.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []int64) (int64, error) {
				command := query.Use(tx).CommandLog
				info, err := command.WithContext(ctx).Where(command.ID.In(ids...), command.StartedAt.Lt(cutoff)).Delete()
				return info.RowsAffected, err
			},
		)
		result.CommandsDeleted += deleted
		if err != nil {
			return fmt.Errorf("command age retention: %w", err)
		}
	}
	if policy.CommandMaxCount > 0 {
		deleted, err := retentionDeleteBatches(ctx, s, "command_log", "count",
			func(tx *gorm.DB) ([]int64, error) {
				command := query.Use(tx).CommandLog
				ids := make([]int64, 0, retentionBatchSize)
				err := command.WithContext(ctx).Order(command.StartedAt.Desc(), command.ID.Desc()).Limit(retentionBatchSize).Offset(int(policy.CommandMaxCount)).Pluck(command.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []int64) (int64, error) {
				command := query.Use(tx).CommandLog
				info, err := command.WithContext(ctx).Where(command.ID.In(ids...)).Delete()
				return info.RowsAffected, err
			},
		)
		result.CommandsDeleted += deleted
		if err != nil {
			return fmt.Errorf("command count retention: %w", err)
		}
	}
	if policy.AIRunMaxAge > 0 || policy.AIRunMaxCount > 0 {
		if err := s.cleanupRetentionHITL(ctx); err != nil {
			return err
		}
	}
	noPendingHITL := field.NewUnsafeFieldRaw("NOT EXISTS (SELECT 1 FROM ai_hitl_run WHERE ai_hitl_run.id = ai_run.id)")
	if policy.AIRunMaxAge > 0 {
		cutoff := now.Add(-policy.AIRunMaxAge).UnixMilli()
		deleted, err := retentionDeleteBatches(ctx, s, "ai_run", "age",
			func(tx *gorm.DB) ([]string, error) {
				run := query.Use(tx).RunRow
				ids := make([]string, 0, retentionBatchSize)
				err := run.WithContext(ctx).
					Where(run.FinishedAt.IsNotNull(), run.FinishedAt.Lt(cutoff)).Where(noPendingHITL).
					Order(run.ID.Asc()).Limit(retentionBatchSize).Pluck(run.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []string) (int64, error) {
				run := query.Use(tx).RunRow
				info, err := run.WithContext(ctx).
					Where(run.ID.In(ids...), run.FinishedAt.IsNotNull(), run.FinishedAt.Lt(cutoff)).Where(noPendingHITL).Delete()
				return info.RowsAffected, err
			},
		)
		result.AIRunsDeleted += deleted
		if err != nil {
			return fmt.Errorf("AI run age retention: %w", err)
		}
	}
	if policy.AIRunMaxCount > 0 {
		deleted, err := retentionDeleteBatches(ctx, s, "ai_run", "count",
			func(tx *gorm.DB) ([]string, error) {
				run := query.Use(tx).RunRow
				ids := make([]string, 0, retentionBatchSize)
				err := run.WithContext(ctx).
					Where(run.FinishedAt.IsNotNull()).Where(noPendingHITL).
					Order(run.FinishedAt.Desc(), run.ID.Desc()).
					Limit(retentionBatchSize).Offset(int(policy.AIRunMaxCount)).Pluck(run.ID, &ids)
				return ids, err
			},
			func(tx *gorm.DB, ids []string) (int64, error) {
				run := query.Use(tx).RunRow
				info, err := run.WithContext(ctx).
					Where(run.ID.In(ids...), run.FinishedAt.IsNotNull()).Where(noPendingHITL).Delete()
				return info.RowsAffected, err
			},
		)
		result.AIRunsDeleted += deleted
		if err != nil {
			return fmt.Errorf("AI run count retention: %w", err)
		}
	}
	return nil
}

func (s *Store) cleanupRetentionHITL(ctx context.Context) error {
	finishedHITL := field.NewUnsafeFieldRaw("EXISTS (SELECT 1 FROM ai_run WHERE ai_run.id = ai_hitl_run.id AND ai_run.finished_at IS NOT NULL)")
	if _, err := retentionDeleteBatches(ctx, s, "ai_hitl_run", "finished",
		func(tx *gorm.DB) ([]string, error) {
			ids := make([]string, 0, retentionBatchSize)
			err := tx.WithContext(ctx).Model(&HitlRunRow{}).
				Table("ai_hitl_run AS hitl").
				Joins("JOIN ai_run AS run ON run.id = hitl.id").
				Where("run.finished_at IS NOT NULL").
				Order("hitl.id").Limit(retentionBatchSize).Pluck("hitl.id", &ids).Error
			return ids, err
		},
		func(tx *gorm.DB, ids []string) (int64, error) {
			hitl := query.Use(tx).HitlRunRow
			info, err := hitl.WithContext(ctx).Where(hitl.ID.In(ids...)).Where(finishedHITL).Delete()
			return info.RowsAffected, err
		},
	); err != nil {
		return fmt.Errorf("terminal HITL cleanup: %w", err)
	}
	orphanHITL := field.NewUnsafeFieldRaw("NOT EXISTS (SELECT 1 FROM ai_run WHERE ai_run.id = ai_hitl_run.id)")
	_, err := retentionDeleteBatches(ctx, s, "ai_hitl_run", "orphan",
		func(tx *gorm.DB) ([]string, error) {
			hitl := query.Use(tx).HitlRunRow
			ids := make([]string, 0, retentionBatchSize)
			err := hitl.WithContext(ctx).Where(orphanHITL).Order(hitl.ID.Asc()).Limit(retentionBatchSize).Pluck(hitl.ID, &ids)
			return ids, err
		},
		func(tx *gorm.DB, ids []string) (int64, error) {
			hitl := query.Use(tx).HitlRunRow
			info, err := hitl.WithContext(ctx).Where(hitl.ID.In(ids...)).Where(orphanHITL).Delete()
			return info.RowsAffected, err
		},
	)
	if err != nil {
		return fmt.Errorf("orphan HITL cleanup: %w", err)
	}
	return nil
}

type retentionBatchOutcome struct {
	selected int
	deleted  int64
}

func retentionDeleteBatches[T any](
	ctx context.Context,
	s *Store,
	table string,
	operation string,
	selectIDs func(*gorm.DB) ([]T, error),
	deleteIDs func(*gorm.DB, []T) (int64, error),
) (int64, error) {
	var total int64
	for batch := 1; ; batch++ {
		outcome, err := s.retentionBatch(ctx, table, operation, batch, func(tx *gorm.DB) (retentionBatchOutcome, error) {
			ids, err := selectIDs(tx)
			if err != nil {
				return retentionBatchOutcome{}, err
			}
			outcome := retentionBatchOutcome{selected: len(ids)}
			if len(ids) == 0 {
				return outcome, nil
			}
			outcome.deleted, err = deleteIDs(tx, ids)
			return outcome, err
		})
		total += outcome.deleted
		if err != nil {
			return total, err
		}
		if outcome.selected < retentionBatchSize || outcome.deleted == 0 {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func (s *Store) retentionBatch(
	ctx context.Context,
	table string,
	operation string,
	batch int,
	fn func(*gorm.DB) (retentionBatchOutcome, error),
) (retentionBatchOutcome, error) {
	started := time.Now()
	tx := s.db.WithContext(ctx).Begin()
	beginWait := time.Since(started)
	if tx.Error != nil {
		s.logRetentionBatch(table, operation, batch, retentionBatchOutcome{}, beginWait, time.Since(started), tx.Error)
		return retentionBatchOutcome{}, tx.Error
	}
	outcome, err := fn(tx)
	if err != nil {
		_ = tx.Rollback().Error
		s.logRetentionBatch(table, operation, batch, outcome, beginWait, time.Since(started), err)
		return retentionBatchOutcome{}, err
	}
	if err := tx.Commit().Error; err != nil {
		s.logRetentionBatch(table, operation, batch, outcome, beginWait, time.Since(started), err)
		return retentionBatchOutcome{}, err
	}
	s.logRetentionBatch(table, operation, batch, outcome, beginWait, time.Since(started), nil)
	return outcome, nil
}

func (s *Store) logRetentionBatch(
	table string,
	operation string,
	batch int,
	outcome retentionBatchOutcome,
	beginWait time.Duration,
	duration time.Duration,
	err error,
) {
	if err == nil && outcome.selected == 0 && outcome.deleted == 0 {
		return
	}
	waitField := "begin_wait_ms"
	if s.backend == BackendSQLite {
		waitField = "lock_wait_ms"
	}
	args := []any{
		"table", table,
		"operation", operation,
		"batch", batch,
		"selected", outcome.selected,
		"deleted", outcome.deleted,
		waitField, retentionMilliseconds(beginWait),
		"duration_ms", retentionMilliseconds(duration),
	}
	if err != nil {
		s.logger.Warn("retention batch failed", append(args, "error", err)...)
		return
	}
	s.logger.Info("retention batch committed", args...)
}

func (s *Store) recordRetentionResult(err error) {
	now := time.Now().UTC()
	s.retention.Lock()
	s.retention.status.LastAttemptAt = now
	if err != nil {
		s.retention.status.LastError = err.Error()
	} else {
		s.retention.status.LastSuccessAt = now
		s.retention.status.LastError = ""
	}
	s.retention.Unlock()
	if err != nil {
		s.logger.Error("store retention cleanup failed", "error", err)
	}
}

func (s *Store) RetentionStatus() RetentionStatus {
	s.retention.RLock()
	defer s.retention.RUnlock()
	return s.retention.status
}
