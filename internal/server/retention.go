package server

import (
	"context"
	"time"
)

type RetentionStatus struct {
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
}

type RetentionStatusProvider interface {
	RetentionStatus(context.Context) (RetentionStatus, error)
}

type RetentionStatusFunc func(context.Context) (RetentionStatus, error)

func (f RetentionStatusFunc) RetentionStatus(ctx context.Context) (RetentionStatus, error) {
	return f(ctx)
}

type RetentionConfig struct {
	Enabled bool
	Status  RetentionStatusProvider
}

type RetentionHealth struct {
	Enabled       bool       `json:"enabled"`
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
	LastError     string     `json:"lastError,omitempty"`
}

func (c *RetentionConfig) health(ctx context.Context) (*RetentionHealth, error) {
	health := &RetentionHealth{Enabled: c.Enabled}
	if c.Status == nil {
		return health, nil
	}
	status, err := c.Status.RetentionStatus(ctx)
	health.LastAttemptAt = retentionTimestamp(status.LastAttemptAt)
	health.LastSuccessAt = retentionTimestamp(status.LastSuccessAt)
	health.LastError = status.LastError
	if err != nil {
		health.LastError = err.Error()
	}
	return health, err
}

func retentionTimestamp(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}
