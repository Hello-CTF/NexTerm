//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
)

type productionUpgradeHTTPResponse struct {
	status  int
	body    map[string]any
	cookies []*http.Cookie
}

func postProductionUpgradeJSON(t *testing.T, client *http.Client, url string, payload any) productionUpgradeHTTPResponse {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result := productionUpgradeHTTPResponse{status: response.StatusCode, cookies: response.Cookies()}
	if err := json.NewDecoder(response.Body).Decode(&result.body); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProductionUpgradePreservesLegacyAccountAndSecretsDatabase(t *testing.T) {
	ctx := context.Background()
	fixture := dbtest.NewFixture(t)
	dataDir := t.TempDir()
	if fixture.Backend == store.BackendSQLite {
		fixture.DSN = filepath.Join(dataDir, "data.db")
	}
	database := fixture.OpenStore(t)
	totpKeyPath := filepath.Join(dataDir, "totp.key")
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(totpKeyPath))
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const adminPassword = "legacy-admin-password-1"
	admin, err := accounts.InitSuperadmin(ctx, code, "legacy-admin", adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(ctx, "legacy-user", "", "legacy-user-password-1"); err != nil {
		t.Fatal(err)
	}
	totpSecret, _, err := accounts.BeginTOTPSetup(ctx, admin.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.ConfirmTOTPSetup(ctx, admin.ID, remoteTestTOTPCode(t, totpSecret, time.Now())); err != nil {
		t.Fatal(err)
	}

	const vaultPassword = "legacy-vault-password-1"
	oldVault := vault.Load(ctx, database)
	if err := oldVault.InitMaster(ctx, vaultPassword); err != nil {
		t.Fatal(err)
	}
	const credentialSecret = "synthetic-legacy-credential-secret"
	nonce, blob, err := oldVault.EncryptCredential(ctx, credentialSecret)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := database.CredentialPut(ctx, store.CredentialInput{
		ID: "legacy-credential", Name: "legacy credential", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: oldVault.KEKHint(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&store.AssetRow{ID: "legacy-asset", Kind: "ssh", Name: "legacy asset", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := database.GORM().Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	var logs syncedLogBuffer
	application, err := NewProduction(ctx, ProductionConfig{
		Config:  Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))},
		DataDir: dataDir,
		DB:      store.BackendConfig{Backend: fixture.Backend, DSN: fixture.DSN},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	adminCtx := ipc.WithUserID(ctx, admin.ID)
	for _, check := range []struct {
		model any
		id    string
	}{
		{&store.AssetRow{}, "legacy-asset"},
		{&store.CredentialRow{}, credentialID},
	} {
		var ownerID string
		if err := application.Services.Store.GORM().Model(check.model).Select("owner_id").Where("id = ?", check.id).Scan(&ownerID).Error; err != nil {
			t.Fatal(err)
		}
		if ownerID != admin.ID {
			t.Fatalf("%T owner = %q, want first active superadmin", check.model, ownerID)
		}
	}

	credential, err := application.Services.Store.CredentialGetRow(adminCtx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if application.Services.Vault.Status().Unlocked {
		t.Fatal("server vault must remain locked until the old master password is supplied")
	}
	if _, err := application.Services.Vault.DecryptCredentialString(ctx, credential); ipc.NormalizeError(err).Code != ipc.CodeVaultLocked {
		t.Fatalf("locked credential decrypt error = %v, want vault_locked", err)
	}
	if err := application.Services.Vault.UnlockMaster(ctx, vaultPassword); err != nil {
		t.Fatal(err)
	}
	plaintext, err := application.Services.Vault.DecryptCredentialString(ctx, credential)
	if err != nil || plaintext != credentialSecret {
		t.Fatalf("upgraded credential plaintext = %q, err=%v", plaintext, err)
	}
	accounts = account.New(
		application.Services.Store.GORM(),
		account.WithTOTPKeyFile(totpKeyPath),
		account.WithActiveSuperadminListener(application.RetryDeferredUpgrade),
	)
	api, err := server.New(server.Config{
		Options:    server.Options{Auth: server.AuthOn, Listen: "127.0.0.1:0", DataDir: dataDir},
		Dispatcher: application.Dispatcher,
		Accounts:   accounts,
		Channels:   server.NewHubAdapter(application.Services.Sessions.Hub()),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = api.Close()
	})
	client := &http.Client{Timeout: 10 * time.Second}
	passwordLogin := postProductionUpgradeJSON(t, client, httpServer.URL+"/auth/login", map[string]any{
		"username": "legacy-user", "password": "legacy-user-password-1",
	})
	passwordCSRF, _ := passwordLogin.body["csrf_token"].(string)
	if passwordLogin.status != http.StatusOK || passwordCSRF == "" || len(passwordLogin.cookies) == 0 {
		t.Fatalf("old password login = %d %+v cookies=%v", passwordLogin.status, passwordLogin.body, passwordLogin.cookies)
	}
	mfaLogin := postProductionUpgradeJSON(t, client, httpServer.URL+"/auth/login", map[string]any{
		"username": "legacy-admin", "password": adminPassword,
	})
	if mfaLogin.status != http.StatusOK || mfaLogin.body["mfa_required"] != true || mfaLogin.body["ticket"] == "" {
		t.Fatalf("old admin password/TOTP challenge = %d %+v", mfaLogin.status, mfaLogin.body)
	}
	ticket, _ := mfaLogin.body["ticket"].(string)
	totpLogin := postProductionUpgradeJSON(t, client, httpServer.URL+"/auth/totp/login", map[string]any{
		"ticket": ticket, "code": remoteTestTOTPCode(t, totpSecret, time.Now()),
	})
	if totpLogin.status != http.StatusOK || totpLogin.body["csrf_token"] == "" || len(totpLogin.cookies) == 0 {
		t.Fatalf("old TOTP login = %d %+v cookies=%v", totpLogin.status, totpLogin.body, totpLogin.cookies)
	}

	application.RetryDeferredUpgrade(ctx)
	if count := strings.Count(logs.String(), "completed data upgrade"); count != 1 {
		t.Fatalf("startup data upgrade log count = %d, logs=%s", count, logs.String())
	}
	for _, secret := range []string{adminPassword, vaultPassword, credentialSecret, totpSecret, admin.ID} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("upgrade logs contain a secret or owner identifier %q", secret)
		}
	}
}

func TestProductionUpgradeSharedModesKeepOwnerless(t *testing.T) {
	for _, test := range []struct {
		name    string
		desktop bool
		open    bool
	}{
		{name: "desktop", desktop: true},
		{name: "open", open: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.open {
				ctx = owner.WithOpenAccess(ctx)
			}
			dataDir := t.TempDir()
			database, err := store.Open(ctx, filepath.Join(dataDir, "data.db"))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []any{
				&store.AppUser{ID: "admin", Username: "admin", Role: "superadmin", PasswordHash: "synthetic-hash", State: "active", CreatedAt: 1, UpdatedAt: 1},
				&store.AssetRow{ID: "shared-asset", Kind: "ssh", Name: "shared", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
				&store.ConversationRow{ID: "shared-conversation", ScopeJSON: "{}", CreatedAt: 1, UpdatedAt: 1},
				&store.CronJob{ID: "shared-cron", SessionID: "shared-conversation", Prompt: "synthetic-prompt", Schedule: "* * * * *", Timezone: "UTC", Enabled: false, TimeoutMS: 1000, CreatedAt: 1, UpdatedAt: 1, Revision: 1, NextRunAt: 1},
				&store.Setting{Key: store.LayoutKey, Value: "shared-layout", UpdatedAt: 1},
			} {
				if err := database.GORM().Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			application, err := NewProduction(ctx, ProductionConfig{
				Config: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, DataDir: dataDir, Desktop: test.desktop,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = application.Services.Store.Close() })
			for _, check := range []struct {
				model any
				id    string
			}{
				{&store.AssetRow{}, "shared-asset"},
				{&store.ConversationRow{}, "shared-conversation"},
				{&store.CronJob{}, "shared-cron"},
			} {
				var ownerID string
				if err := application.Services.Store.GORM().Model(check.model).Select("owner_id").Where("id = ?", check.id).Scan(&ownerID).Error; err != nil {
					t.Fatal(err)
				}
				if ownerID != "" {
					t.Fatalf("%T owner = %q, want shared ownerless behavior", check.model, ownerID)
				}
			}
			for _, key := range []string{store.LayoutSettingKeyForUser("admin")} {
				if _, found, err := application.Services.Store.SettingGet(ctx, key); err != nil || found {
					t.Fatalf("shared migration created user setting %s: found=%v err=%v", key, found, err)
				}
			}
		})
	}
}

func TestProductionUpgradeStartupFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.AssetRow{ID: "legacy-asset", Kind: "ssh", Name: "legacy", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Callback().Query().Before("gorm:query").Register("nexterm_test:production_upgrade_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "app_user" {
			tx.AddError(errors.New("forced production upgrade read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	coordinator := &productionUpgradeCoordinator{database: database, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := coordinator.migrate(ctx); err == nil || !strings.Contains(err.Error(), "forced production upgrade read failure") {
		t.Fatalf("startup migration error = %v, want injected failure", err)
	}
}

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
