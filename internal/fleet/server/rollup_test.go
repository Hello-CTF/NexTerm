package fleetserver

import (
	"context"
	"testing"
	"time"
)

func TestRollupRunnerSchedulesMetricsRetention(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "rollup-owner")
	deviceID, _ := enrollAgent(t, f, owner, "rollup-box")

	// 插入一条超过原始保留期 (48h) 的原始指标, 等聚合进小时桶并清理。
	old := time.Now().Add(-49 * time.Hour).UnixMilli()
	if err := f.service.RecordMetrics(context.Background(), deviceID, MetricsSample{
		TS: old, CPUPct: 20, MemUsed: 1, MemTotal: 2, DiskUsed: 3, DiskTotal: 4, UptimeS: 5,
	}); err != nil {
		t.Fatal(err)
	}

	runner := NewRollupRunner(f.service, 50*time.Millisecond, nil)
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runner.Shutdown(shutdownCtx); err != nil {
			t.Fatal(err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !runner.Status().LastSuccessAt.IsZero() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	status := runner.Status()
	if status.LastSuccessAt.IsZero() {
		t.Fatalf("rollup never succeeded: %+v", status)
	}
	if status.LastError != "" {
		t.Fatalf("rollup last error: %s", status.LastError)
	}

	var hourlyCount, rawCount int
	if err := f.service.db.QueryRowContext(context.Background(), "SELECT count(*) FROM device_metrics_hourly WHERE device_id = ?", deviceID).Scan(&hourlyCount); err != nil {
		t.Fatal(err)
	}
	if err := f.service.db.QueryRowContext(context.Background(), "SELECT count(*) FROM device_metrics WHERE device_id = ?", deviceID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if hourlyCount != 1 || rawCount != 0 {
		t.Fatalf("hourly=%d raw=%d, want 1/0", hourlyCount, rawCount)
	}
	if err := runner.Start(context.Background()); err == nil {
		t.Fatal("double start must fail")
	}
}

func TestRollupRunnerShutdownWithoutStart(t *testing.T) {
	f := newHTTPFixture(t, false)
	runner := NewRollupRunner(f.service, time.Hour, nil)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown without Start = %v", err)
	}
}
