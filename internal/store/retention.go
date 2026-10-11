package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
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
}

type RetentionResult struct {
	AuditDeleted      int64 `json:"auditDeleted"`
	RecordingsDeleted int64 `json:"recordingsDeleted"`
	AIRunsDeleted     int64 `json:"aiRunsDeleted"`
	CommandsDeleted   int64 `json:"commandsDeleted"`
}

type RetentionStatus struct {
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	LastSuccessAt time.Time `json:"lastSuccessAt"`
	LastError     string    `json:"lastError,omitempty"`
}

type retentionState struct {
	sync.RWMutex
	status RetentionStatus
}

func (p RetentionPolicy) validate() error {
	for name, value := range map[string]time.Duration{
		"audit max age":     p.AuditMaxAge,
		"recording max age": p.RecordingMaxAge,
		"AI run max age":    p.AIRunMaxAge,
		"command max age":   p.CommandMaxAge,
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	for name, value := range map[string]int64{
		"audit max count":     p.AuditMaxCount,
		"recording max count": p.RecordingMaxCount,
		"AI run max count":    p.AIRunMaxCount,
		"command max count":   p.CommandMaxCount,
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
		p.CommandMaxAge == 0 && p.CommandMaxCount == 0
}

func (s *Store) EnforceRetention(ctx context.Context, policy RetentionPolicy) (result RetentionResult, returnErr error) {
	defer func() { s.recordRetentionResult(returnErr) }()
	if err := policy.validate(); err != nil {
		return RetentionResult{}, badParam(err)
	}
	if policy.empty() {
		return RetentionResult{}, nil
	}
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		audit := queries.AuditRow
		if policy.AuditMaxAge > 0 {
			info, err := audit.WithContext(ctx).Where(audit.TS.Lt(now.Add(-policy.AuditMaxAge).UnixMilli())).Delete()
			result.AuditDeleted = info.RowsAffected
			if err != nil {
				return fmt.Errorf("audit age retention: %w", err)
			}
		}
		if policy.AuditMaxCount > 0 {
			ids := []int64{}
			if err := audit.WithContext(ctx).
				Order(audit.TS.Desc(), audit.ID.Desc()).
				Limit(-1).
				Offset(int(policy.AuditMaxCount)).
				Pluck(audit.ID, &ids); err != nil {
				return fmt.Errorf("audit count retention: %w", err)
			}
			if len(ids) > 0 {
				info, err := audit.WithContext(ctx).Where(audit.ID.In(ids...)).Delete()
				result.AuditDeleted += info.RowsAffected
				if err != nil {
					return fmt.Errorf("audit count retention: %w", err)
				}
			}
		}
		recording := queries.RecordingRow
		if policy.RecordingMaxAge > 0 {
			info, err := recording.WithContext(ctx).
				Where(recording.EndedAt.IsNotNull(), recording.EndedAt.Lt(now.Add(-policy.RecordingMaxAge).UnixMilli())).
				Delete()
			result.RecordingsDeleted = info.RowsAffected
			if err != nil {
				return fmt.Errorf("recording age retention: %w", err)
			}
		}
		if policy.RecordingMaxCount > 0 {
			ids := []string{}
			if err := recording.WithContext(ctx).
				Where(recording.EndedAt.IsNotNull()).
				Order(recording.EndedAt.Desc(), recording.ID.Desc()).
				Limit(-1).
				Offset(int(policy.RecordingMaxCount)).
				Pluck(recording.ID, &ids); err != nil {
				return fmt.Errorf("recording count retention: %w", err)
			}
			if len(ids) > 0 {
				info, err := recording.WithContext(ctx).Where(recording.ID.In(ids...)).Delete()
				result.RecordingsDeleted += info.RowsAffected
				if err != nil {
					return fmt.Errorf("recording count retention: %w", err)
				}
			}
		}
		command := queries.CommandLog
		if policy.CommandMaxAge > 0 {
			info, err := command.WithContext(ctx).
				Where(command.StartedAt.Lt(now.Add(-policy.CommandMaxAge).UnixMilli())).
				Delete()
			result.CommandsDeleted = info.RowsAffected
			if err != nil {
				return fmt.Errorf("command age retention: %w", err)
			}
		}
		if policy.CommandMaxCount > 0 {
			ids := []int64{}
			if err := command.WithContext(ctx).
				Order(command.StartedAt.Desc(), command.ID.Desc()).
				Limit(-1).
				Offset(int(policy.CommandMaxCount)).
				Pluck(command.ID, &ids); err != nil {
				return fmt.Errorf("command count retention: %w", err)
			}
			if len(ids) > 0 {
				info, err := command.WithContext(ctx).Where(command.ID.In(ids...)).Delete()
				result.CommandsDeleted += info.RowsAffected
				if err != nil {
					return fmt.Errorf("command count retention: %w", err)
				}
			}
		}
		run := queries.RunRow
		hitl := queries.HitlRunRow
		if policy.AIRunMaxAge > 0 || policy.AIRunMaxCount > 0 {
			finishedIDs := []string{}
			if err := run.WithContext(ctx).Where(run.FinishedAt.IsNotNull()).Pluck(run.ID, &finishedIDs); err != nil {
				return fmt.Errorf("terminal HITL cleanup: %w", err)
			}
			if len(finishedIDs) > 0 {
				if _, err := hitl.WithContext(ctx).Where(hitl.ID.In(finishedIDs...)).Delete(); err != nil {
					return fmt.Errorf("terminal HITL cleanup: %w", err)
				}
			}
			runIDs := []string{}
			if err := run.WithContext(ctx).Pluck(run.ID, &runIDs); err != nil {
				return fmt.Errorf("orphan HITL cleanup: %w", err)
			}
			if len(runIDs) > 0 {
				if _, err := hitl.WithContext(ctx).Where(hitl.ID.NotIn(runIDs...)).Delete(); err != nil {
					return fmt.Errorf("orphan HITL cleanup: %w", err)
				}
			} else if _, err := hitl.WithContext(ctx).Where(hitl.ID.IsNotNull()).Delete(); err != nil {
				return fmt.Errorf("orphan HITL cleanup: %w", err)
			}
		}
		pendingHITL := []string{}
		if policy.AIRunMaxAge > 0 || policy.AIRunMaxCount > 0 {
			if err := hitl.WithContext(ctx).Pluck(hitl.ID, &pendingHITL); err != nil {
				return fmt.Errorf("AI run retention: %w", err)
			}
		}
		if policy.AIRunMaxAge > 0 {
			q := run.WithContext(ctx).
				Where(run.FinishedAt.IsNotNull(), run.FinishedAt.Lt(now.Add(-policy.AIRunMaxAge).UnixMilli()))
			if len(pendingHITL) > 0 {
				q = q.Where(run.ID.NotIn(pendingHITL...))
			}
			info, err := q.Delete()
			result.AIRunsDeleted = info.RowsAffected
			if err != nil {
				return fmt.Errorf("AI run age retention: %w", err)
			}
		}
		if policy.AIRunMaxCount > 0 {
			q := run.WithContext(ctx).
				Where(run.FinishedAt.IsNotNull()).
				Order(run.FinishedAt.Desc(), run.ID.Desc()).
				Limit(-1).
				Offset(int(policy.AIRunMaxCount))
			if len(pendingHITL) > 0 {
				q = q.Where(run.ID.NotIn(pendingHITL...))
			}
			ids := []string{}
			if err := q.Pluck(run.ID, &ids); err != nil {
				return fmt.Errorf("AI run count retention: %w", err)
			}
			if len(ids) > 0 {
				info, err := run.WithContext(ctx).Where(run.ID.In(ids...)).Delete()
				result.AIRunsDeleted += info.RowsAffected
				if err != nil {
					return fmt.Errorf("AI run count retention: %w", err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return RetentionResult{}, dbError(err)
	}
	return result, nil
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
