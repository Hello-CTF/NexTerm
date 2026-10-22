package production

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

type productionUpgradeEventCapture struct {
	mu     sync.Mutex
	events []ipc.Event
}

func (c *productionUpgradeEventCapture) Emit(_ context.Context, event ipc.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *productionUpgradeEventCapture) appErrorMessages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var messages []string
	for _, event := range c.events {
		if event.Event != ipc.TopicAppError {
			continue
		}
		if payload, ok := event.Payload.(ipc.AppErrorEvent); ok && payload.Code == "data_upgrade" {
			messages = append(messages, payload.Message)
		}
	}
	return messages
}

func TestProductionOwnerlessMigrationDefersAndRetriesBeforeScheduler(t *testing.T) {
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

	var logs syncedLogBuffer
	application, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, DataDir: dataDir, Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Services.Store.Close() })
	var deferred store.CronJob
	if err := application.Services.Store.GORM().Where("id = ?", "job-orphan").First(&deferred).Error; err != nil {
		t.Fatal(err)
	}
	if deferred.OwnerID != "" || deferred.OwnerKind != "" || !deferred.Enabled || deferred.LastError != "" {
		t.Fatalf("ownerless cron was quarantined while no admin existed: %+v", deferred)
	}
	if deferred.RunID != "" || deferred.LeaseOwner != "" {
		t.Fatalf("cron was claimed before application Start: %+v", deferred)
	}
	if !strings.Contains(logs.String(), "data upgrade deferred") || strings.Contains(logs.String(), "sensitive-startup-prompt") {
		t.Fatalf("deferred startup logs = %s", logs.String())
	}

	accounts := account.New(application.Services.Store.GORM(), account.WithActiveSuperadminListener(application.RetryDeferredUpgrade))
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := accounts.InitSuperadmin(ctx, code, "first-admin", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	var migrated store.CronJob
	if err := application.Services.Store.GORM().Where("id = ?", "job-orphan").First(&migrated).Error; err != nil {
		t.Fatal(err)
	}
	if migrated.OwnerID != admin.ID || cron.OwnerKind(migrated.OwnerKind) != cron.OwnerKindAccount || !migrated.Enabled || migrated.LastError != "" {
		t.Fatalf("cron after first active admin = %+v", migrated)
	}
	if migrated.RunID != "" || migrated.LeaseOwner != "" {
		t.Fatalf("retry ran the scheduler instead of only migrating: %+v", migrated)
	}
	var conversation store.ConversationRow
	if err := application.Services.Store.GORM().Where("id = ?", "conv-orphan").First(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	if conversation.OwnerID != admin.ID {
		t.Fatalf("conversation owner = %q, want %q", conversation.OwnerID, admin.ID)
	}

	beforeLogs := logs.String()
	if err := accounts.SetUserDisabled(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserDisabled(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}
	var repeated store.CronJob
	if err := application.Services.Store.GORM().Where("id = ?", "job-orphan").First(&repeated).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repeated, migrated) || logs.String() != beforeLogs {
		t.Fatalf("non-deferred admin notification repeated migration:\nbefore=%+v\nafter=%+v\nlogs=%s", migrated, repeated, logs.String())
	}
}

func TestProductionDeferredUpgradeFailureIsVisibleAndRetries(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.AssetRow{ID: "legacy-asset", Kind: "ssh", Name: "legacy", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	var logs syncedLogBuffer
	events := &productionUpgradeEventCapture{}
	application, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(&logs, nil)), Events: events}, DataDir: dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Services.Store.Close() })
	callbackName := "nexterm_test:production_deferred_upgrade_failure"
	if err := application.Services.Store.GORM().Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "asset" {
			tx.AddError(errors.New("forced deferred production upgrade failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	accounts := account.New(application.Services.Store.GORM(), account.WithActiveSuperadminListener(application.RetryDeferredUpgrade))
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := accounts.InitSuperadmin(ctx, code, "first-admin", "synthetic-password-1")
	if err != nil {
		t.Fatalf("committed admin creation must not fail because upgrade retry failed: %v", err)
	}
	if err := application.Services.Store.GORM().Callback().Update().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "data upgrade retry after active superadmin creation failed") {
		t.Fatalf("retry failure was not logged: %s", logs.String())
	}
	messages := events.appErrorMessages()
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "重试失败") {
		t.Fatalf("retry failure was not emitted: %v", messages)
	}
	var ownerID string
	if err := application.Services.Store.GORM().Model(&store.AssetRow{}).Select("owner_id").Where("id = ?", "legacy-asset").Scan(&ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if ownerID != "" {
		t.Fatalf("failed retry partially assigned asset owner %q", ownerID)
	}

	if err := accounts.SetUserDisabled(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserDisabled(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := application.Services.Store.GORM().Model(&store.AssetRow{}).Select("owner_id").Where("id = ?", "legacy-asset").Scan(&ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if ownerID != admin.ID {
		t.Fatalf("asset owner after successful retry = %q, want %q", ownerID, admin.ID)
	}
}

func TestProductionUpgradeConflictIsVisible(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&store.AppUser{ID: "admin", Username: "admin", Role: "superadmin", PasswordHash: "synthetic-hash", State: "active", CreatedAt: 1, UpdatedAt: 1},
		&store.KnownHostRow{ID: "legacy", Host: "sensitive-conflict-host", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "legacy-fingerprint", AddedAt: 1},
		&store.KnownHostRow{ID: "target", Host: "sensitive-conflict-host", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "target-fingerprint", AddedAt: 2, OwnerID: "admin"},
	} {
		if err := database.GORM().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	var logs syncedLogBuffer
	events := &productionUpgradeEventCapture{}
	application, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(&logs, nil)), Events: events}, DataDir: dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Services.Store.Close() })
	if !strings.Contains(logs.String(), "data upgrade completed with preserved or unattended entries") || !strings.Contains(logs.String(), "ownership_conflicts=1") {
		t.Fatalf("conflict was not visible in logs: %s", logs.String())
	}
	if strings.Contains(logs.String(), "sensitive-conflict-host") || strings.Contains(logs.String(), "fingerprint") {
		t.Fatalf("conflict log leaked record content: %s", logs.String())
	}
	messages := events.appErrorMessages()
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "冲突") {
		t.Fatalf("conflict was not emitted: %v", messages)
	}
	var ownerID string
	if err := application.Services.Store.GORM().Model(&store.KnownHostRow{}).Select("owner_id").Where("id = ?", "legacy").Scan(&ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if ownerID != "" {
		t.Fatalf("conflicting source was silently overwritten or reassigned: %q", ownerID)
	}
}
