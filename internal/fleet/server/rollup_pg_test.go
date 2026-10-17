package fleetserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func TestRollupMetricsPostgres(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	accounts := account.New(database.GORM())
	service, err := New(Config{DB: database.GORM(), Accounts: accounts})
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateUser(ctx, "pg-rollup-owner", "", "pw-123456")
	if err != nil {
		t.Fatal(err)
	}
	device, err := accounts.RegisterDevice(ctx, user.ID, "pg-rollup-box", "agent")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-49 * time.Hour).UnixMilli()
	if err := service.RecordMetrics(ctx, device.ID, MetricsSample{TS: old, CPUPct: 20, MemUsed: 1, MemTotal: 2, DiskUsed: 3, DiskTotal: 4, UptimeS: 5}); err != nil {
		t.Fatal(err)
	}
	if err := service.RollupMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	hourly := query.Use(database.GORM()).DeviceMetricsHourly
	hourlyRows, err := hourly.WithContext(ctx).Where(hourly.DeviceID.Eq(device.ID)).Find()
	if err != nil {
		t.Fatal(err)
	}
	if len(hourlyRows) != 1 || hourlyRows[0].SampleCount != 1 || hourlyRows[0].BucketTS != (old/3600000)*3600000 {
		t.Fatalf("hourly = %+v", hourlyRows)
	}
	raw := query.Use(database.GORM()).DeviceMetrics
	rawCount, err := raw.WithContext(ctx).Where(raw.DeviceID.Eq(device.ID)).Count()
	if err != nil {
		t.Fatal(err)
	}
	if rawCount != 0 {
		t.Fatalf("raw left = %d", rawCount)
	}
}

func TestDeviceMetricsPostgres(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	accounts := account.New(database.GORM())
	now := time.Now().UnixMilli()
	service, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(func() int64 { return now }))
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateUser(ctx, "pg-metrics-owner", "", "pw-123456")
	if err != nil {
		t.Fatal(err)
	}
	device, err := accounts.RegisterDevice(ctx, user.ID, "pg-metrics-box", "agent")
	if err != nil {
		t.Fatal(err)
	}
	const rowCount = maxMetricsSamples + 2
	base := now - time.Hour.Milliseconds()
	rows := make([]*store.DeviceMetrics, 0, rowCount)
	for i := rowCount - 1; i >= 0; i-- {
		sample := MetricsSample{TS: base + int64(i/2), CPUPct: float64(i), MemUsed: 50, MemTotal: 100, DiskUsed: 25, DiskTotal: 100, UptimeS: int64(i)}
		rows = append(rows, deviceMetricRowForTest(device.ID, fmt.Sprintf("metric-%06d", i), sample))
	}
	deviceMetrics := query.Use(database.GORM()).DeviceMetrics
	if err := deviceMetrics.WithContext(ctx).CreateInBatches(rows, 500); err != nil {
		t.Fatal(err)
	}

	metrics, err := service.DeviceMetrics(ctx, &account.Identity{UserID: user.ID, Role: user.Role}, device.ID, base)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.SampleCount != rowCount || !metrics.Downsampled || len(metrics.Samples) == 0 || len(metrics.Samples) > maxMetricsSamples {
		t.Fatalf("metrics count=%d returned=%d downsampled=%v", metrics.SampleCount, len(metrics.Samples), metrics.Downsampled)
	}
	if metrics.Latest == nil || metrics.Latest.CPUPct != rowCount-1 || metrics.Samples[0].CPUPct != 0 || metrics.Samples[len(metrics.Samples)-1].CPUPct != rowCount-1 {
		t.Fatalf("latest/endpoints=%+v %+v %+v", metrics.Latest, metrics.Samples[0], metrics.Samples[len(metrics.Samples)-1])
	}
	for i, sample := range metrics.Samples {
		if i > 0 {
			previous := metrics.Samples[i-1]
			if previous.TS > sample.TS || (previous.TS == sample.TS && previous.CPUPct > sample.CPUPct) {
				t.Fatalf("samples are not in stable (ts,id) order at %d", i)
			}
		}
	}
}
