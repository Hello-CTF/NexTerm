package production

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const productionMigrationAdmin = "first-admin"

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

func TestProductionLegacyAISettingsMigrationLogsCountsWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	putProductionMemoryMigrationUser(t, database, productionMigrationAdmin, "superadmin", "active", 1)
	legacyProfile := productionMigrationProfile("p-legacy", "legacy-name-not-log", "legacy-log-secret")
	targetProfile := productionMigrationProfile("p-legacy", "target-name-not-log", "target-log-secret")
	newProfile := productionMigrationProfile("p-new", "new-name-not-log", "new-log-secret")
	if err := database.GORM().Create(&store.Setting{
		Key: store.AIProfilesSettingKey, Value: productionLegacyProfileSetting(t, legacyProfile, newProfile), UpdatedAt: 100,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.Setting{
		Key: store.AIProfilesSettingKeyForUser(productionMigrationAdmin), Value: productionLegacyProfileSetting(t, targetProfile), UpdatedAt: 200,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := migrateProductionLegacyAISettings(ctx, database, logger, false); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, want := range []string{"migrated=1", "conflicts=1", "ledger_entries=2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("migration log is missing count %q: %s", want, output)
		}
	}
	for _, forbidden := range []string{
		"legacy-log-secret", "target-log-secret", "new-log-secret",
		"legacy-name-not-log", "target-name-not-log", "new-name-not-log", productionMigrationAdmin,
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("migration log contains forbidden value %q", forbidden)
		}
	}
	logSize := logs.Len()
	if err := migrateProductionLegacyAISettings(ctx, database, logger, false); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != logSize {
		t.Fatal("idempotent reentry emitted another migration report")
	}
}

func TestProductionLegacyAISettingsMigrationRunsAtStartup(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	putProductionMemoryMigrationUser(t, database, productionMigrationAdmin, "superadmin", "active", 1)
	const masterPassword = "startup-test-password"
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	const legacySecret = "startup-secret"
	legacyProfile := productionMigrationProfile("p-startup", "startup", legacySecret)
	legacyProfile.APIKey, err = credentialVault.EncryptSecret(ctx, legacySecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(legacyProfile.APIKey, store.SecretEnvelopePrefix) {
		t.Fatal("legacy fixture did not use an encrypted secret envelope")
	}
	if err := database.GORM().Create(&store.Setting{
		Key: store.AIProfilesSettingKey, Value: productionLegacyProfileSetting(t, legacyProfile), UpdatedAt: 100,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	application, err := NewProduction(ctx, ProductionConfig{
		Config: Config{Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}, DataDir: dataDir, Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := application.Services.Vault.UnlockMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	adminCtx := ipc.WithUserID(ctx, productionMigrationAdmin)
	overview, err := application.Services.Profiles.Overview(adminCtx)
	if err != nil || len(overview.Profiles) != 1 || overview.Profiles[0].ID != legacyProfile.ID {
		t.Fatalf("startup migrated profile overview = %+v, err = %v", overview, err)
	}
	config, ok, err := application.Services.Profiles.ActiveConfig(adminCtx)
	if err != nil || !ok || config.APIKey != legacySecret {
		t.Fatalf("migrated profile key is not usable by the target owner: ok = %v, err = %v", ok, err)
	}
	if _, found, err := application.Services.Store.SettingGet(ctx, store.AIProfilesSettingKey); err != nil || !found {
		t.Fatalf("startup removed the original legacy setting: found = %v, err = %v", found, err)
	}
}
