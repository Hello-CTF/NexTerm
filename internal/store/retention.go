package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

type RetentionPolicy struct {
	AuditMaxAge       time.Duration
	AuditMaxCount     int64
	RecordingMaxAge   time.Duration
	RecordingMaxCount int64
}

type RetentionResult struct {
	AuditDeleted      int64 `json:"auditDeleted"`
	RecordingsDeleted int64 `json:"recordingsDeleted"`
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
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	for name, value := range map[string]int64{
		"audit max count":     p.AuditMaxCount,
		"recording max count": p.RecordingMaxCount,
	} {
		if value < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	return nil
}

func (s *Store) EnforceRetention(ctx context.Context, policy RetentionPolicy) (result RetentionResult, returnErr error) {
	defer func() { s.recordRetentionResult(returnErr) }()
	if err := policy.validate(); err != nil {
		return RetentionResult{}, badParam(err)
	}
	if policy.AuditMaxAge == 0 && policy.AuditMaxCount == 0 &&
		policy.RecordingMaxAge == 0 && policy.RecordingMaxCount == 0 {
		return RetentionResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now()

	if policy.AuditMaxAge > 0 {
		cutoff := now.Add(-policy.AuditMaxAge).UnixMilli()
		result.AuditDeleted, err = retentionDelete(ctx, tx,
			"DELETE FROM audit_log WHERE ts < ?", cutoff)
		if err != nil {
			return RetentionResult{}, fmt.Errorf("audit age retention: %w", dbError(err))
		}
	}
	if policy.AuditMaxCount > 0 {
		var deleted int64
		deleted, err = retentionDelete(ctx, tx, `DELETE FROM audit_log WHERE id IN (
	SELECT id FROM audit_log ORDER BY ts DESC, id DESC LIMIT -1 OFFSET ?
)`, policy.AuditMaxCount)
		result.AuditDeleted += deleted
		if err != nil {
			return RetentionResult{}, fmt.Errorf("audit count retention: %w", dbError(err))
		}
	}
	if policy.RecordingMaxAge > 0 {
		cutoff := now.Add(-policy.RecordingMaxAge).UnixMilli()
		var deleted int64
		deleted, err = retentionDelete(ctx, tx,
			"DELETE FROM terminal_recording WHERE ended_at IS NOT NULL AND ended_at < ?", cutoff)
		result.RecordingsDeleted = deleted
		if err != nil {
			return RetentionResult{}, fmt.Errorf("recording age retention: %w", dbError(err))
		}
	}
	if policy.RecordingMaxCount > 0 {
		var deleted int64
		deleted, err = retentionDelete(ctx, tx, `DELETE FROM terminal_recording
WHERE ended_at IS NOT NULL AND id IN (
	SELECT id FROM terminal_recording WHERE ended_at IS NOT NULL
	ORDER BY ended_at DESC, id DESC LIMIT -1 OFFSET ?
)`, policy.RecordingMaxCount)
		result.RecordingsDeleted += deleted
		if err != nil {
			return RetentionResult{}, fmt.Errorf("recording count retention: %w", dbError(err))
		}
	}
	if err := tx.Commit(); err != nil {
		return RetentionResult{}, dbError(err)
	}
	return result, nil
}

func retentionDelete(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
