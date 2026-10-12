package fleetserver

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
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
