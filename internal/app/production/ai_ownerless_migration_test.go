package production

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestProductionOwnerlessMigrationRunsBeforeScheduler(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.ConversationRow{
		{ID: "conv-owned", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: "owner-a"},
		{ID: "conv-orphan", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := database.GORM().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	nextRun := time.Now().Add(-time.Minute).UnixMilli()
	for _, row := range []store.CronJob{
		{ID: "job-owned", SessionID: "conv-owned", Prompt: "sensitive-startup-prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: false, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: nextRun},
		{ID: "job-orphan", SessionID: "conv-orphan", Prompt: "sensitive-startup-prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: nextRun},
	} {
		if err := database.GORM().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	application, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, DataDir: dataDir, Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := false
	t.Cleanup(func() {
		if started {
			_ = application.Shutdown(context.Background())
			return
		}
		_ = application.Services.Store.Close()
	})
	for id, want := range map[string]struct {
		ownerID string
		kind    cron.OwnerKind
		enabled bool
	}{
		"job-owned":  {ownerID: "owner-a", kind: cron.OwnerKindAccount, enabled: false},
		"job-orphan": {ownerID: "", kind: cron.OwnerKindLegacy, enabled: false},
	} {
		var row store.CronJob
		if err := application.Services.Store.GORM().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.OwnerID != want.ownerID || cron.OwnerKind(row.OwnerKind) != want.kind || row.Enabled != want.enabled {
			t.Fatalf("startup migrated cron %s = %+v, want %+v", id, row, want)
		}
		if row.RunID != "" || row.LeaseOwner != "" {
			t.Fatalf("cron %s was claimed before application Start: %+v", id, row)
		}
	}
	migrationLogs := logs.String()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	started = true
	rows, err := application.Services.Store.ConvList(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("anonymous account-mode conversations after startup = %+v, %v", rows, err)
	}
	admin := ipc.WithRole(ipc.WithUserID(ctx, "admin"), "superadmin")
	rows, err = application.Services.Store.ConvList(admin)
	if err != nil || len(rows) != 1 || rows[0].ID != "conv-orphan" {
		t.Fatalf("superadmin quarantine conversations after startup = %+v, %v", rows, err)
	}
	if strings.Contains(migrationLogs, "sensitive-startup-prompt") {
		t.Fatalf("startup migration logged record content: %s", migrationLogs)
	}
}
