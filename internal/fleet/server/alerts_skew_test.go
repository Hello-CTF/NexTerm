package fleetserver

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAlertClockSkewDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	accounts := account.New(database.GORM())
	fixture := &serviceFixture{accounts: accounts, db: database.GORM(), now: time.Now().UnixMilli()}
	service, err := New(Config{DB: database.GORM(), Accounts: accounts}, WithNow(func() int64 { return fixture.now }))
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = service
	alice := fixture.createUser(t, "alice")
	result := fixture.enrollAgent(t, alice, "skew-host")
	ctx := context.Background()
	record := func(ts int64) {
		t.Helper()
		if err := fixture.service.RecordMetrics(ctx, result.DeviceID, MetricsSample{TS: ts, CPUPct: 10}); err != nil {
			t.Fatal(err)
		}
	}
	stored := func() []int64 {
		t.Helper()
		var rows []store.DeviceMetrics
		if err := fixture.db.Where("device_id = ?", result.DeviceID).Order("ts ASC").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out := make([]int64, len(rows))
		for i, r := range rows {
			out[i] = r.TS
		}
		return out
	}
	skew := func() int64 {
		t.Helper()
		got, err := fixture.service.DeviceAlertSettings(ctx, fixture.identity(alice), result.DeviceID)
		if err != nil {
			t.Fatal(err)
		}
		return got.ClockSkewMS
	}
	ten := (10 * time.Minute).Milliseconds()
	now := fixture.now

	if skew() != 0 {
		t.Fatalf("skew before any sample = %d", skew())
	}
	record(now + ten)
	if got := stored(); len(got) != 1 || got[0] != now {
		t.Fatalf("future sample stored as %v, want server time %d", got, now)
	}
	if skew() != ten {
		t.Fatalf("skew = %d, want %d", skew(), ten)
	}
	fixture.now = now + 1000
	record(fixture.now - ten)
	if got := stored(); len(got) != 2 || got[1] != fixture.now {
		t.Fatalf("past sample stored as %v", got)
	}
	if skew() != -ten {
		t.Fatalf("skew = %d, want %d", skew(), -ten)
	}
	// Within one sync interval the device time is kept and the report clears.
	fixture.now = now + 2000
	near := fixture.now - 59_000
	record(near)
	if got := stored(); !slices.Contains(got, near) {
		t.Fatalf("near sample stored as %v, want %d kept", got, near)
	}
	if skew() != 0 {
		t.Fatalf("skew after a normal sample = %d", skew())
	}
	// Observations older than an hour are not reported.
	record(fixture.now + ten)
	if skew() == 0 {
		t.Fatal("skew not reported")
	}
	fixture.now += (61 * time.Minute).Milliseconds()
	if skew() != 0 {
		t.Fatalf("stale skew = %d", skew())
	}
}

// seedMetric inserts a sample with its own timestamp, bypassing the device clock check of RecordMetrics.
// Tests that need historic or future rows use it.
func seedMetric(s *Service, ctx context.Context, deviceID string, sample MetricsSample) error {
	row := store.DeviceMetrics{
		ID: ids.New(), DeviceID: deviceID, TS: sample.TS,
		CPUPct: &sample.CPUPct, MemUsed: &sample.MemUsed, MemTotal: &sample.MemTotal,
		DiskUsed: &sample.DiskUsed, DiskTotal: &sample.DiskTotal, UptimeS: &sample.UptimeS,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}
