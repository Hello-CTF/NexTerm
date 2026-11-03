package upgrade_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
	"gorm.io/gorm"
)

func putFleetAudit(t *testing.T, db *gorm.DB, source, kind, assetID string) int64 {
	t.Helper()
	row := &store.AuditRow{TS: 1, Source: source, Kind: kind, PayloadJSON: "{}"}
	if assetID != "" {
		row.AssetID = &assetID
	}
	createUpgradeRow(t, db, row)
	return row.ID
}

func putUpgradeDevice(t *testing.T, db *gorm.DB, id, userID string) {
	t.Helper()
	createUpgradeRow(t, db, &store.UserDevice{ID: id, UserID: userID, Name: id, Kind: "agent", CreatedAt: 1})
}

func TestMigrateBackfillsDeviceAuditOwnersDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t).GORM()
	ctx := context.Background()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	putUpgradeUser(t, db, "alice", "user", "active", 2)
	putUpgradeUser(t, db, "bob", "user", "active", 3)
	putUpgradeDevice(t, db, "dev-a", "alice")
	putUpgradeDevice(t, db, "dev-b", "bob")

	want := map[int64]string{
		putFleetAudit(t, db, "fleet", "device_online", "dev-a"):   "alice",
		putFleetAudit(t, db, "fleet", "device_offline", "dev-a"):  "alice",
		putFleetAudit(t, db, "fleet", "device_failover", "dev-b"): "bob",
		putFleetAudit(t, db, "fleet", "device_online", "dev-b"):   "bob",
	}

	report, err := upgrade.Migrate(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceAuditOwners != int64(len(want)) || report.Ownership.Assigned != 0 || report.OwnerID != "" {
		t.Fatalf("report = %+v", report)
	}
	for id, owner := range want {
		requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(id), owner)
	}

	again, err := upgrade.Migrate(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.DeviceAuditOwners != 0 || again.OwnerID != "" {
		t.Fatalf("second report = %+v", again)
	}
	for id, owner := range want {
		requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(id), owner)
	}
}

func TestMigrateDeviceAuditBackfillSkipsUnmatchedRowsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t).GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	putUpgradeUser(t, db, "alice", "user", "active", 2)
	putUpgradeDevice(t, db, "dev-a", "alice")
	missing := putFleetAudit(t, db, "fleet", "device_online", "dev-gone")
	otherSource := putFleetAudit(t, db, "ssh", "device_online", "dev-a")
	matched := putFleetAudit(t, db, "fleet", "device_online", "dev-a")

	report, err := upgrade.Migrate(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceAuditOwners != 1 || report.OwnerID != "admin" {
		t.Fatalf("report = %+v", report)
	}
	requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(matched), "alice")
	requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(missing), "admin")
	requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(otherSource), "admin")
}

func TestMigrateSharedSkipsDeviceAuditBackfillDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t).GORM()
	putUpgradeUser(t, db, "alice", "user", "active", 2)
	putUpgradeDevice(t, db, "dev-a", "alice")
	id := putFleetAudit(t, db, "fleet", "device_online", "dev-a")

	report, err := upgrade.Migrate(context.Background(), db, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceAuditOwners != 0 {
		t.Fatalf("report = %+v", report)
	}
	requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(id), "")
}

func putOwnedFleetAudit(t *testing.T, db *gorm.DB, owner, kind, assetID string) int64 {
	t.Helper()
	row := &store.AuditRow{TS: 1, OwnerID: owner, Source: "fleet", Kind: kind, AssetID: &assetID, PayloadJSON: "{}"}
	createUpgradeRow(t, db, row)
	return row.ID
}

func TestMigrateCorrectsMisattributedDeviceAuditDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t).GORM()
	ctx := context.Background()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	putUpgradeUser(t, db, "alice", "user", "active", 2)
	putUpgradeUser(t, db, "bob", "user", "active", 3)
	putUpgradeDevice(t, db, "dev-a", "alice")
	putUpgradeDevice(t, db, "dev-b", "bob")

	fixed := map[int64]string{
		putOwnedFleetAudit(t, db, "admin", "device_online", "dev-a"):   "alice",
		putOwnedFleetAudit(t, db, "admin", "device_offline", "dev-a"):  "alice",
		putOwnedFleetAudit(t, db, "admin", "device_failover", "dev-b"): "bob",
		putOwnedFleetAudit(t, db, "alice", "device_online", "dev-b"):   "bob",
	}
	untouched := map[int64]string{
		putOwnedFleetAudit(t, db, "admin", "device_terminal", "dev-a"):  "admin",
		putOwnedFleetAudit(t, db, "admin", "device_online", "dev-gone"): "admin",
		putOwnedFleetAudit(t, db, "alice", "device_online", "dev-a"):    "alice",
	}

	report, err := upgrade.Migrate(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceAuditOwners != int64(len(fixed)) {
		t.Fatalf("report = %+v", report)
	}
	check := func() {
		t.Helper()
		for id, owner := range fixed {
			requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(id), owner)
		}
		for id, owner := range untouched {
			requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(id), owner)
		}
	}
	check()
	again, err := upgrade.Migrate(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.DeviceAuditOwners != 0 {
		t.Fatalf("second report = %+v", again)
	}
	check()
}

func TestMigrateSharedKeepsDeviceAuditOwnersDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t).GORM()
	putUpgradeUser(t, db, "admin", "superadmin", "active", 1)
	putUpgradeUser(t, db, "alice", "user", "active", 2)
	putUpgradeDevice(t, db, "dev-a", "alice")
	id := putOwnedFleetAudit(t, db, "admin", "device_online", "dev-a")

	report, err := upgrade.Migrate(context.Background(), db, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceAuditOwners != 0 {
		t.Fatalf("report = %+v", report)
	}
	requireUpgradeOwner(t, db, &store.AuditRow{}, "id", itoa(id), "admin")
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
