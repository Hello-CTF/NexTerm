package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const testGatewayKey = "gateway-secret"

// newPlatformFixture assembles a platform-managed mode instance (with a gateway header), isomorphic to a Lazycat deployment:
// listening on a non-loopback address, access control delegated to the gateway header, empty account database.
func newPlatformFixture(t *testing.T) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = AuthPlatform
	config.Options.Listen = "0.0.0.0:8080"
	config.GatewayAuthKey = testGatewayKey
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))
	config.Accounts = accounts
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts, db: database.DB(),
		client: &http.Client{},
	}
}

func TestPlatformModeRequiresGatewayKey(t *testing.T) {
	config := testConfig(t, false)
	config.Options.Auth = AuthPlatform
	config.Options.Listen = "0.0.0.0:8080"
	if _, err := New(config); err == nil {
		t.Fatal("platform mode without a gateway key must fail at startup; otherwise no one can enter and no one knows why")
	}
}

// TestPlatformSessionEstablishesOwner covers the full code-free path on Lazycat:
// gateway header => automatically becomes the platform owner => gets a session => the frontend never shows login/init UI.
func TestPlatformSessionEstablishesOwner(t *testing.T) {
	fixture := newPlatformFixture(t)
	gateway := map[string]string{GatewayAuthHeader: testGatewayKey}

	if call := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("session exchange without gateway header = %d, want %d", call.status, http.StatusForbidden)
	}

	call := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, nil, "", gateway)
	if call.status != http.StatusOK {
		t.Fatalf("platform session = %d body=%v", call.status, call.body)
	}
	session := captureSession(t, call)
	if username, _ := session.user["username"].(string); username != account.PlatformOwnerUsername {
		t.Fatalf("owner username = %q, want %q", username, account.PlatformOwnerUsername)
	}
	if role, _ := session.user["role"].(string); role != string(account.RoleSuperadmin) {
		t.Fatalf("owner role = %q, want %q", role, account.RoleSuperadmin)
	}

	ownerID, _ := session.user["id"].(string)
	secret, _, err := fixture.accounts.BeginTOTPSetup(context.Background(), ownerID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.accounts.ConfirmTOTPSetup(context.Background(), ownerID, testTOTPCode(t, secret, time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	if err := fixture.accounts.SetMFARequired(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	again := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, session, "", gateway)
	if again.status != http.StatusOK {
		t.Fatalf("reused platform session = %d body=%v", again.status, again.body)
	}
	reused, _ := again.body["user"].(map[string]any)
	if id, _ := reused["id"].(string); id != session.user["id"] {
		t.Fatalf("platform owner was created more than once: %v != %v", reused["id"], session.user["id"])
	}
	if again.body["mfa_required"] != true || reused["mfa_enabled"] != true {
		t.Fatalf("reused session MFA fields = %v, %v", again.body["mfa_required"], reused["mfa_enabled"])
	}
	if count, err := fixture.accounts.CountUsers(context.Background()); err != nil || count != 1 {
		t.Fatalf("account count = %d err=%v, want 1", count, err)
	}

	me := fixture.call(t, http.MethodGet, "/auth/me", nil, session, "", gateway)
	if me.status != http.StatusOK {
		t.Fatalf("/auth/me = %d body=%v", me.status, me.body)
	}
	meUser, _ := me.body["user"].(map[string]any)
	if me.body["mfa_required"] != again.body["mfa_required"] || meUser["mfa_enabled"] != reused["mfa_enabled"] {
		t.Fatalf("reused session and /auth/me MFA fields differ: %v, %v", again.body, me.body)
	}

	init := fixture.call(t, http.MethodPost, "/auth/init", map[string]any{"code": "whatever"}, nil, "", gateway)
	if init.status != http.StatusForbidden {
		t.Fatalf("/auth/init in platform mode = %d, want %d", init.status, http.StatusForbidden)
	}

	status := fixture.call(t, http.MethodGet, "/auth/status", nil, nil, "", gateway)
	if status.status != http.StatusOK {
		t.Fatalf("/auth/status = %d", status.status)
	}
	if auth, _ := status.body["auth"].(string); auth != AuthPlatform {
		t.Fatalf("auth = %q, want %q", auth, AuthPlatform)
	}
	if initialized, _ := status.body["initialized"].(bool); !initialized {
		t.Fatal("initialized must be true after platform owner creation; otherwise the frontend returns to the initialization wizard")
	}
}

// TestPlatformModeStillRefusesForeignGatewayKey confirms the gateway header is still a hard gate:
// a request with a different key gets neither a session nor access to session-requiring routes.
func TestPlatformModeStillRefusesForeignGatewayKey(t *testing.T) {
	fixture := newPlatformFixture(t)
	forged := map[string]string{GatewayAuthHeader: "not-the-key"}

	if call := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, nil, "", forged); call.status != http.StatusForbidden {
		t.Fatalf("session exchange with forged gateway header = %d, want %d", call.status, http.StatusForbidden)
	}
	if count, err := fixture.accounts.CountUsers(context.Background()); err != nil || count != 0 {
		t.Fatalf("forged request must not create an account: count=%d err=%v", count, err)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/devices", nil, nil, "", forged); call.status != http.StatusUnauthorized {
		t.Fatalf("device list with forged gateway header = %d, want %d", call.status, http.StatusUnauthorized)
	}
}
