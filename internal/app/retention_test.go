package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type fakeRetentionStore struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	status    store.RetentionStatus
	enforce   func(context.Context, int) error
}

func (s *fakeRetentionStore) EnforceRetention(ctx context.Context, _ store.RetentionPolicy) (store.RetentionResult, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	var err error
	if s.enforce != nil {
		err = s.enforce(ctx, call)
	}
	s.mu.Lock()
	s.active--
	s.status.LastAttemptAt = time.Now().UTC()
	if err != nil {
		s.status.LastError = err.Error()
	} else {
		s.status.LastSuccessAt = time.Now().UTC()
		s.status.LastError = ""
	}
	s.mu.Unlock()
	return store.RetentionResult{}, err
}

func (s *fakeRetentionStore) RetentionStatus() store.RetentionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *fakeRetentionStore) snapshot() (calls, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.maxActive
}

func retentionTestRunner(t *testing.T, stored RetentionStore, policy store.RetentionPolicy) *RetentionRunner {
	t.Helper()
	runner, err := NewRetentionRunner(RetentionConfig{
		Store: stored,
		Policy: RetentionPolicySourceFunc(func(context.Context) (store.RetentionPolicy, error) {
			return policy, nil
		}),
		Interval:       time.Hour,
		AttemptTimeout: time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func waitRetention(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("retention condition was not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStoreRetentionPolicyFromAppSettings(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	source := StoreRetentionPolicy(database)
	policy, err := source.RetentionPolicy(ctx)
	if err != nil || retentionEnabled(policy) {
		t.Fatalf("missing setting policy = %+v, %v", policy, err)
	}
	if err := database.SettingSet(ctx, RetentionSettingKey, `{"auditMaxAgeMs":60000,"auditMaxCount":3,"recordingMaxAgeMs":120000,"recordingMaxCount":4}`); err != nil {
		t.Fatal(err)
	}
	policy, err = source.RetentionPolicy(ctx)
	want := store.RetentionPolicy{AuditMaxAge: time.Minute, AuditMaxCount: 3, RecordingMaxAge: 2 * time.Minute, RecordingMaxCount: 4}
	if err != nil || policy != want {
		t.Fatalf("configured policy = %+v, %v; want %+v", policy, err, want)
	}
	for _, raw := range []string{`{`, `{"auditMaxAgeMs":-1}`, `{"recordingMaxCount":-1}`, `{"auditMaxAgeMs":9223372036854775807}`} {
		if err := database.SettingSet(ctx, RetentionSettingKey, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := source.RetentionPolicy(ctx); err == nil {
			t.Fatalf("expected invalid setting rejection: %s", raw)
		}
	}
	if _, err := retentionDuration("overflow", math.MaxInt64); err == nil {
		t.Fatal("expected duration overflow rejection")
	}
}

func TestRetentionRunnerDisabledDoesNotDelete(t *testing.T) {
	stored := &fakeRetentionStore{}
	runner := retentionTestRunner(t, stored, store.RetentionPolicy{})
	if err := runner.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitRetention(t, func() bool {
		status, _ := runner.Status(context.Background())
		return status.LastAttemptAt != nil
	})
	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := stored.snapshot()
	if calls != 0 || status.Enabled || status.LastError != "" {
		t.Fatalf("disabled retention calls = %d, status = %+v", calls, status)
	}
	if err := runner.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionRunnerFailureRecoveryAndHealthComposition(t *testing.T) {
	stored := &fakeRetentionStore{}
	stored.enforce = func(_ context.Context, call int) error {
		if call == 1 {
			return errors.New("injected scheduled failure")
		}
		return nil
	}
	runner := retentionTestRunner(t, stored, store.RetentionPolicy{AuditMaxCount: 1})
	ticks := make(chan time.Time, 1)
	runner.after = func(time.Duration) <-chan time.Time { return ticks }
	application, err := New(Config{
		RetentionStatus: runner.Status,
		Modules:         []Module{{Name: "retention", Component: runner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	waitRetention(t, func() bool {
		calls, _ := stored.snapshot()
		return calls == 1
	})
	health := application.health(context.Background(), ServeConfig{})
	if health.Retention == nil || !health.Retention.Enabled || !strings.Contains(health.Retention.LastError, "injected scheduled failure") || health.Retention.LastSuccessAt != nil {
		t.Fatalf("failure health = %+v", health.Retention)
	}
	encoded, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"retention"`) || !strings.Contains(string(encoded), "injected scheduled failure") {
		t.Fatalf("health JSON = %s", encoded)
	}
	ticks <- time.Now()
	waitRetention(t, func() bool {
		calls, _ := stored.snapshot()
		return calls >= 2
	})
	waitRetention(t, func() bool {
		status, _ := runner.Status(context.Background())
		return status.LastError == "" && status.LastSuccessAt != nil
	})
}

func TestRetentionRunnerDoesNotOverlapAndCancelsAttempt(t *testing.T) {
	t.Run("non-overlapping", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		stored := &fakeRetentionStore{}
		stored.enforce = func(ctx context.Context, call int) error {
			if call == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		runner := retentionTestRunner(t, stored, store.RetentionPolicy{AuditMaxCount: 1})
		ticks := make(chan time.Time, 1)
		runner.after = func(time.Duration) <-chan time.Time { return ticks }
		if err := runner.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		<-started
		ticks <- time.Now()
		time.Sleep(20 * time.Millisecond)
		calls, maxActive := stored.snapshot()
		if calls != 1 || maxActive != 1 {
			t.Fatalf("blocked cleanup calls = %d, max active = %d", calls, maxActive)
		}
		close(release)
		waitRetention(t, func() bool {
			calls, _ := stored.snapshot()
			return calls >= 2
		})
		_, maxActive = stored.snapshot()
		if maxActive != 1 {
			t.Fatalf("overlapping cleanup count = %d", maxActive)
		}
		if err := runner.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("shutdown cancellation", func(t *testing.T) {
		started := make(chan struct{})
		stored := &fakeRetentionStore{}
		stored.enforce = func(ctx context.Context, _ int) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		runner := retentionTestRunner(t, stored, store.RetentionPolicy{AuditMaxCount: 1})
		if err := runner.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		<-started
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runner.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runner.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	})
}
