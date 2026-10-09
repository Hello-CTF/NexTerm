package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRetentionHealthDisabledSuccessAndFailureStates(t *testing.T) {
	t.Run("unconfigured is not reported as disabled", func(t *testing.T) {
		_, httpServer := newTestHTTP(t, testConfig(t, false))
		if health := readServerHealth(t, httpServer); health.Retention != nil {
			t.Fatalf("retention = %+v, want null", health.Retention)
		}
	})

	t.Run("disabled has no fabricated timestamps", func(t *testing.T) {
		config := testConfig(t, false)
		config.Retention = &RetentionConfig{Enabled: false}
		_, httpServer := newTestHTTP(t, config)
		retention := readServerHealth(t, httpServer).Retention
		if retention == nil || retention.Enabled || retention.LastAttemptAt != nil || retention.LastSuccessAt != nil || retention.LastError != "" {
			t.Fatalf("disabled retention = %+v", retention)
		}
	})

	attempt := time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	success := attempt.Add(time.Second)
	t.Run("success timestamps pass through", func(t *testing.T) {
		config := testConfig(t, false)
		config.Retention = &RetentionConfig{Enabled: true, Status: RetentionStatusFunc(func(context.Context) (RetentionStatus, error) {
			return RetentionStatus{LastAttemptAt: attempt, LastSuccessAt: success}, nil
		})}
		_, httpServer := newTestHTTP(t, config)
		retention := readServerHealth(t, httpServer).Retention
		if retention == nil || !retention.Enabled || retention.LastAttemptAt == nil || !retention.LastAttemptAt.Equal(attempt) || retention.LastSuccessAt == nil || !retention.LastSuccessAt.Equal(success) || retention.LastError != "" {
			t.Fatalf("success retention = %+v", retention)
		}
	})

	t.Run("cleanup failure remains visible", func(t *testing.T) {
		config := testConfig(t, false)
		config.Retention = &RetentionConfig{Enabled: true, Status: RetentionStatusFunc(func(context.Context) (RetentionStatus, error) {
			return RetentionStatus{LastAttemptAt: attempt, LastError: "injected cleanup failure"}, nil
		})}
		_, httpServer := newTestHTTP(t, config)
		retention := readServerHealth(t, httpServer).Retention
		if retention == nil || retention.LastError != "injected cleanup failure" || retention.LastAttemptAt == nil || retention.LastSuccessAt != nil {
			t.Fatalf("failure retention = %+v", retention)
		}
	})

	t.Run("provider failure remains visible", func(t *testing.T) {
		config := testConfig(t, false)
		config.Retention = &RetentionConfig{Enabled: true, Status: RetentionStatusFunc(func(context.Context) (RetentionStatus, error) {
			return RetentionStatus{LastAttemptAt: attempt}, errors.New("status backend unavailable")
		})}
		_, httpServer := newTestHTTP(t, config)
		health := readServerHealth(t, httpServer)
		if !health.OK || health.Retention == nil || health.Retention.LastError != "status backend unavailable" || health.Retention.LastAttemptAt == nil || health.Retention.LastSuccessAt != nil {
			t.Fatalf("provider failure health = %+v", health)
		}
	})
}

func TestRetentionHealthRealStoreDisabledSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if result, err := db.EnforceRetention(ctx, store.RetentionPolicy{}); err != nil || result != (store.RetentionResult{}) {
		t.Fatalf("disabled retention result = %+v, %v", result, err)
	}
	config := testConfig(t, false)
	config.Retention = &RetentionConfig{Enabled: false, Status: RetentionStatusFunc(func(context.Context) (RetentionStatus, error) {
		status := db.RetentionStatus()
		return RetentionStatus{LastAttemptAt: status.LastAttemptAt, LastSuccessAt: status.LastSuccessAt, LastError: status.LastError}, nil
	})}
	_, httpServer := newTestHTTP(t, config)
	retention := readServerHealth(t, httpServer).Retention
	if retention == nil || retention.Enabled || retention.LastAttemptAt == nil || retention.LastSuccessAt == nil || retention.LastError != "" {
		t.Fatalf("real disabled retention = %+v", retention)
	}
}

func TestEnabledRetentionRequiresStatusProvider(t *testing.T) {
	config := testConfig(t, false)
	config.Retention = &RetentionConfig{Enabled: true}
	if _, err := New(config); err == nil {
		t.Fatal("enabled retention without a status provider was accepted")
	}
}

func readServerHealth(t *testing.T, httpServer *httptest.Server) Health {
	t.Helper()
	response, err := httpServer.Client().Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	var health Health
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	return health
}
