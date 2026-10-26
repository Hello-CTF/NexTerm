package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
)

// syncedLogBuffer is a race-safe log sink: tests poll it while loggers write
// concurrently, which a plain bytes.Buffer would race on.
type syncedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *syncedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

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

func TestProductionOpenToAccountAIHistoryDefersAndRetriesBeforeScheduler(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	nextRun := time.Now().Add(-time.Minute).UnixMilli()
	for _, row := range []any{
		&store.ConversationRow{ID: "conv-open", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.MessageRow{ID: "msg-open", ConversationID: "conv-open", Role: "user", ContentJSON: "{}", CreatedAt: 1, Seq: 1},
		&store.RunRow{ID: "run-open", ConversationID: "conv-open", Status: "done", CreatedAt: 1, UpdatedAt: 1},
		&store.CronJob{ID: "job-open", SessionID: "conv-open", OwnerKind: string(cron.OwnerKindOpen), Prompt: "sensitive-startup-prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: nextRun},
		&store.CronJob{ID: "job-lost", SessionID: "conv-missing", OwnerKind: string(cron.OwnerKindOpen), Prompt: "sensitive-startup-prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: nextRun},
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
		Config: Config{Logger: slog.New(slog.NewTextHandler(&logs, nil)), Events: events}, DataDir: dataDir, Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Services.Store.Close() })
	loadJob := func(id string) store.CronJob {
		t.Helper()
		var job store.CronJob
		if err := application.Services.Store.GORM().Where("id = ?", id).First(&job).Error; err != nil {
			t.Fatal(err)
		}
		return job
	}
	for _, id := range []string{"job-open", "job-lost"} {
		deferred := loadJob(id)
		if deferred.OwnerID != "" || cron.OwnerKind(deferred.OwnerKind) != cron.OwnerKindOpen || !deferred.Enabled || deferred.LastError != "" {
			t.Fatalf("Open cron changed while no admin existed: %+v", deferred)
		}
		if deferred.RunID != "" || deferred.LeaseOwner != "" {
			t.Fatalf("cron was claimed before application Start: %+v", deferred)
		}
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
	migrated := loadJob("job-open")
	if migrated.OwnerID != admin.ID || cron.OwnerKind(migrated.OwnerKind) != cron.OwnerKindAccount || !migrated.Enabled || migrated.LastError != "" {
		t.Fatalf("Open cron after first active admin = %+v", migrated)
	}
	if migrated.RunID != "" || migrated.LeaseOwner != "" {
		t.Fatalf("retry ran the scheduler instead of only migrating: %+v", migrated)
	}
	adminCtx := ipc.WithUserID(ctx, admin.ID)
	if _, err := scopeForConversation(application.Services.Store)(adminCtx, cron.Trigger{
		JobID: migrated.ID, SessionID: migrated.SessionID, OwnerID: migrated.OwnerID, OwnerKind: cron.OwnerKind(migrated.OwnerKind),
	}); err != nil {
		t.Fatalf("migrated Open cron is not runnable by the admin: %v", err)
	}
	lost := loadJob("job-lost")
	if lost.OwnerID != "" || lost.Enabled || lost.LastError != cron.ErrOpenJobUnattributed.Error() {
		t.Fatalf("unattributable Open cron = %+v, want disabled with a visible error", lost)
	}
	for _, check := range []struct {
		model any
		id    string
	}{
		{&store.ConversationRow{}, "conv-open"},
		{&store.MessageRow{}, "msg-open"},
		{&store.RunRow{}, "run-open"},
	} {
		var ownerID string
		if err := application.Services.Store.GORM().Model(check.model).Select("owner_id").Where("id = ?", check.id).Scan(&ownerID).Error; err != nil {
			t.Fatal(err)
		}
		if ownerID != admin.ID {
			t.Fatalf("%T %s owner = %q, want the first admin", check.model, check.id, ownerID)
		}
	}
	if !strings.Contains(logs.String(), "cron_assigned=1") || !strings.Contains(logs.String(), "cron_disabled=1") || !strings.Contains(logs.String(), "messages_assigned=1") {
		t.Fatalf("Open-to-account AI attribution was not reported: %s", logs.String())
	}
	if messages := events.appErrorMessages(); len(messages) == 0 {
		t.Fatal("disabled Open cron was not surfaced to the user")
	}

	beforeLogs := logs.String()
	if err := accounts.SetUserDisabled(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetUserDisabled(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}
	if repeated := loadJob("job-open"); !reflect.DeepEqual(repeated, migrated) || logs.String() != beforeLogs {
		t.Fatalf("non-deferred admin notification repeated migration:\nbefore=%+v\nafter=%+v\nlogs=%s", migrated, repeated, logs.String())
	}
}

func TestProductionOpenAISettingsCopiedAtStartup(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.AppUser{
		ID: "first-admin", Username: "first-admin", Role: "superadmin", State: "active", PasswordHash: "test", CreatedAt: 1, UpdatedAt: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	const masterPassword = "startup-test-password"
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	const openSecret = "startup-secret"
	openProfile := productionMigrationProfile("p-startup", "startup-name-not-log", openSecret)
	openProfile.APIKey, err = credentialVault.EncryptSecret(ctx, openSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.Setting{
		Key: store.AIProfilesSettingKey, Value: productionLegacyProfileSetting(t, openProfile), UpdatedAt: 100,
	}).Error; err != nil {
		t.Fatal(err)
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
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "ai_settings_migrated=1") {
		t.Fatalf("AI settings copy was not reported: %s", logs.String())
	}
	for _, forbidden := range []string{openSecret, openProfile.APIKey, "startup-name-not-log", "first-admin"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("upgrade log contains forbidden value %q", forbidden)
		}
	}
	if err := application.Services.Vault.UnlockMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	adminCtx := ipc.WithUserID(ctx, "first-admin")
	overview, err := application.Services.Profiles.Overview(adminCtx)
	if err != nil || len(overview.Profiles) != 1 || overview.Profiles[0].ID != openProfile.ID {
		t.Fatalf("copied profile overview = %+v, err = %v", overview, err)
	}
	config, ok, err := application.Services.Profiles.ActiveConfig(adminCtx)
	if err != nil || !ok || config.APIKey != openSecret {
		t.Fatalf("copied profile key is not usable by the admin: ok = %v, err = %v", ok, err)
	}
	if _, found, err := application.Services.Store.SettingGet(ctx, store.AIProfilesSettingKey); err != nil || !found {
		t.Fatalf("startup removed the Open-mode setting: found = %v, err = %v", found, err)
	}
}

func productionLegacyProfileSetting(t *testing.T, values ...profiles.Profile) string {
	t.Helper()
	activeID := values[0].ID
	encoded, err := json.Marshal(struct {
		Version  int                `json:"version"`
		Profiles []profiles.Profile `json:"profiles"`
		ActiveID *string            `json:"activeId"`
	}{Version: profiles.StoreVersion, Profiles: values, ActiveID: &activeID})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func productionMigrationProfile(id, name, secret string) profiles.Profile {
	profile := profiles.DefaultProfile()
	profile.ID = id
	profile.Name = name
	profile.BaseURL = "https://" + name + ".invalid/v1"
	profile.APIKey = secret
	profile.Model = "model-" + id
	return profile
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

func TestProductionOpenToAccountUpgradeWarnsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&store.AppUser{ID: "admin", Username: "admin", Role: "superadmin", PasswordHash: "synthetic-hash", State: "active", CreatedAt: 1, UpdatedAt: 1},
		&store.ConversationRow{ID: "conv-open", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.MessageRow{ID: "msg-open", ConversationID: "conv-open", Role: "user", ContentJSON: "{}", CreatedAt: 1, Seq: 1},
		&store.CronJob{ID: "job-lost", SessionID: "conv-missing", OwnerKind: string(cron.OwnerKindOpen), Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: 1},
		&store.Setting{Key: store.AIProfilesSettingKey, Value: productionLegacyProfileSetting(t, productionMigrationProfile("p-open", "open", "enc:v1:synthetic")), UpdatedAt: 1},
	} {
		if err := database.GORM().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	start := func() (*syncedLogBuffer, *productionUpgradeEventCapture) {
		t.Helper()
		logs := &syncedLogBuffer{}
		events := &productionUpgradeEventCapture{}
		application, err := NewProduction(ctx, ProductionConfig{
			Config: Config{Logger: slog.New(slog.NewTextHandler(logs, nil)), Events: events}, DataDir: dataDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := application.Services.Store.Close(); err != nil {
			t.Fatal(err)
		}
		return logs, events
	}
	firstLogs, firstEvents := start()
	if len(firstEvents.appErrorMessages()) != 1 || !strings.Contains(firstLogs.String(), "cron_disabled=1") {
		t.Fatalf("first start did not surface the disabled Open job: events=%v logs=%s", firstEvents.appErrorMessages(), firstLogs.String())
	}
	secondLogs, secondEvents := start()
	if messages := secondEvents.appErrorMessages(); len(messages) != 0 {
		t.Fatalf("second start repeated the data upgrade error: %v", messages)
	}
	if strings.Contains(secondLogs.String(), "data upgrade") {
		t.Fatalf("second start logged a data upgrade again: %s", secondLogs.String())
	}
}

func TestProductionStartWhileUpgradeDeferredLeavesOpenJobs(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Minute).UnixMilli()
	for _, row := range []any{
		&store.AppUser{ID: "member", Username: "member", Role: "user", PasswordHash: "synthetic-hash", State: "active", CreatedAt: 1, UpdatedAt: 1},
		&store.ConversationRow{ID: "conv-open", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
		&store.ConversationRow{ID: "conv-member", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1, OwnerID: "member"},
		&store.CronJob{ID: "job-open", SessionID: "conv-open", OwnerKind: string(cron.OwnerKindOpen), Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: due},
		// The account job is the positive control: once the scheduler has
		// handled it, the same due scan has seen (and skipped) the Open job.
		&store.CronJob{ID: "job-member", SessionID: "conv-member", OwnerID: "member", OwnerKind: string(cron.OwnerKindAccount), Prompt: "prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: true, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: due},
	} {
		if err := database.GORM().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	application, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(&syncedLogBuffer{}, nil))}, DataDir: dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	gormDB := application.Services.Store.GORM()
	var before store.CronJob
	if err := gormDB.Where("id = ?", "job-open").First(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var handled int64
		if err := gormDB.Model(&store.CronJob{}).
			Where("id = ? AND (run_id <> '' OR last_scheduled_for IS NOT NULL)", "job-member").
			Count(&handled).Error; err != nil {
			t.Fatal(err)
		}
		if handled == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not handle the account job")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := application.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, dataDir+"/data.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var after store.CronJob
	if err := reopened.GORM().Where("id = ?", "job-open").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("scheduler touched the Open job while the upgrade was deferred:\nbefore %+v\nafter  %+v", before, after)
	}
}
