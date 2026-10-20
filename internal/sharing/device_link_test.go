package sharing

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

func TestDeviceShareLinkTableSurvivesMigration(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := database.GORM()
	if db.Migrator().HasTable("device_share_link") {
		t.Fatal("fresh migration still creates the retired device_share_link table")
	}

	if err := db.Exec(`CREATE TABLE device_share_link (id varchar(191) PRIMARY KEY, token_hash text NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO device_share_link (id, token_hash) VALUES (?, ?)`, "legacy-link", "legacy-hash").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels...); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable("device_share_link") {
		t.Fatal("AutoMigrate dropped the legacy device_share_link table")
	}
	var tokenHash string
	if err := db.Raw(`SELECT token_hash FROM device_share_link WHERE id = ?`, "legacy-link").Scan(&tokenHash).Error; err != nil {
		t.Fatal(err)
	}
	if tokenHash != "legacy-hash" {
		t.Fatalf("legacy row token_hash = %q, want preserved value", tokenHash)
	}
}
