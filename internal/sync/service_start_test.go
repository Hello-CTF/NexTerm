package sync

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// probeEventRecorder records sync status events emitted by the startup probe and background rounds.
type probeEventRecorder struct {
	mu     stdsync.Mutex
	events []ipc.Event
}

func (r *probeEventRecorder) emit(_ context.Context, event ipc.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *probeEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func openLinkStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedSavedLink initializes the vault with the given master password and saves a complete link, simulating config left by a previous run.
func seedSavedLink(t *testing.T, db *store.Store, masterPassword, url, username, password string) {
	t.Helper()
	ctx := context.Background()
	seedVault := vault.Load(ctx, db)
	if err := seedVault.InitMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	seed := New(db, seedVault, WithMetadata("test", true))
	if _, err := seed.LinkSet(ctx, LinkPatch{
		URL: testPtr(url), Username: testPtr(username), Password: testPtr(password),
	}); err != nil {
		t.Fatal(err)
	}
}

// restartService re-Loads the vault on the same store and builds a new Service, simulating a desktop process restart:
// the new Engine's session cache is empty and the link config is re-decrypted from settings.
func restartService(t *testing.T, db *store.Store, events *probeEventRecorder) (*Service, *vault.Vault) {
	t.Helper()
	reloaded := vault.Load(context.Background(), db)
	service := New(db, reloaded, WithMetadata("test", true), WithEvents(ipc.EmitterFunc(events.emit)))
	service.syncPeriod = time.Hour
	return service, reloaded
}

func startProbeService(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
}

func waitForCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	waitForConditionTimeout(t, what, 3*time.Second, nil, condition)
}

func waitForConditionTimeout(t *testing.T, what string, timeout time.Duration, detail func() string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			message := "condition not met: " + what
			if detail != nil {
				message += ": " + detail()
			}
			t.Fatal(message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func linkOf(t *testing.T, service *Service) Link {
	t.Helper()
	link, err := service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return link
}

func TestServiceStartAutoConnectsSavedLink(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	seedSavedLink(t, db, "", server.URL, "alice", "alice-password")

	events := &probeEventRecorder{}
	service, reloaded := restartService(t, db, events)
	if !reloaded.Status().Unlocked {
		t.Fatal("passwordless vault should auto-unlock on reload")
	}
	startProbeService(t, service)

	waitForCondition(t, "saved link not auto-connected after restart", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	waitForCondition(t, "startup probe did not broadcast sync status", func() bool {
		return events.count() > 0
	})
	if link := linkOf(t, service); link.LastError != "" {
		t.Fatalf("unexpected last error after auto-connect: %q", link.LastError)
	}
}

func TestServiceStartProbeFailureRecordsLastError(t *testing.T) {
	db := openLinkStore(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	seedSavedLink(t, db, "", deadURL, "alice", "alice-password")

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	// Only the startup probe emits an event in this scenario (the session is never established, and changeless background rounds do not broadcast).
	waitForCondition(t, "startup probe did not run", func() bool {
		return events.count() > 0
	})
	link := linkOf(t, service)
	if !link.IsConfigured() || link.LastError == "" {
		t.Fatalf("link = %+v, want configured with recorded probe error", link)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LoggedIn {
		t.Fatal("logged in against a dead server")
	}
}

func TestServiceStartProbeMFARequiredIsHonest(t *testing.T) {
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	fixture.bindTOTP(t, "alice", "alice-pw-123")
	seedSavedLink(t, db, "", fixture.URL, "alice", "alice-pw-123")

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "MFA account not flagged for interactive re-login", func() bool {
		link, err := service.LinkGet(context.Background())
		return err == nil && link.LastError == errReloginMFA.Error()
	})
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LoggedIn || !status.Configured {
		t.Fatalf("status = %+v, want configured but not logged in", status)
	}
	waitForCondition(t, "startup probe did not broadcast sync status", func() bool {
		return events.count() > 0
	})
}

func TestServiceStartProbeResetRequiredIsHonest(t *testing.T) {
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	userID := fixture.createUser(t, "alice", "temp-pw-123")
	if err := fixture.accounts.AdminResetUser(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	seedSavedLink(t, db, "", fixture.URL, "alice", "temp-pw-123")

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "reset-required account not flagged for interactive re-login", func() bool {
		link, err := service.LinkGet(context.Background())
		return err == nil && link.LastError == errReloginReset.Error()
	})
	if status, err := service.Status(context.Background()); err != nil || status.LoggedIn {
		t.Fatalf("status = %+v, %v, want not logged in", status, err)
	}
}

func TestServiceStartConnectsAfterVaultUnlock(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	seedSavedLink(t, db, "master-pw-123", server.URL, "alice", "alice-password")

	events := &probeEventRecorder{}
	service, reloaded := restartService(t, db, events)
	if reloaded.Status().Unlocked {
		t.Fatal("master-password vault must stay locked after reload")
	}
	startProbeService(t, service)

	// During the locked period both the probe and background rounds silently skip: no events, no recorded errors.
	// Deterministic completion signal instead of a blind sleep: the first background round records its blocked attempt in the round status.
	waitForCondition(t, "background round did not record a blocked attempt while the vault is locked", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.Paused && status.VaultLocked && !status.LastAttemptAt.IsZero()
	})
	if events.count() != 0 {
		t.Fatal("probe must stay silent while the vault is locked")
	}
	if link, err := service.LinkGet(context.Background()); err == nil || link.LastError != "" {
		t.Fatalf("locked vault must not record probe state: %+v, %v", link, err)
	}

	if err := reloaded.UnlockMaster(ctx, "master-pw-123"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "saved link not connected after vault unlock", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	waitForCondition(t, "unlock-triggered connect did not broadcast sync status", func() bool {
		return events.count() > 0
	})
}

// The first login persists {cookie, CSRF, userID, DEK} as an enc:v1: envelope; after a restart the envelope restores the session without login,
// the server sees no further /auth/login, the restored DEK matches what the server envelope unwraps to, and a sync round genuinely works.
func TestServiceStartRestoresPersistedSession(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	server := newTestSyncServer(t)
	dek := server.createUser(t, "alice", "alice-password")
	seedSavedLink(t, db, "", server.URL, "alice", "alice-password")

	raw, found, err := db.SettingGet(ctx, settingLinkSession)
	if err != nil || !found {
		t.Fatalf("persisted session missing after first login: found=%v, %v", found, err)
	}
	if !strings.HasPrefix(raw, store.SecretEnvelopePrefix) {
		t.Fatal("persisted session must be stored as an enc:v1: envelope")
	}

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "persisted session not restored after restart", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	if got := server.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (restart restored the persisted session without /auth/login)", got)
	}
	service.engine.sessionMu.Lock()
	session := service.engine.session
	service.engine.sessionMu.Unlock()
	if session == nil || !bytes.Equal(session.dek, dek) {
		t.Fatal("restored session DEK does not match the server-issued DEK")
	}
	if _, err := service.Sync(ctx); err != nil {
		t.Fatalf("sync with the restored session failed: %v", err)
	}
	if link := linkOf(t, service); link.LastError != "" {
		t.Fatalf("unexpected last error after session restore: %q", link.LastError)
	}
}

// The persistent cookie is revoked server-side (401): the envelope is cleared and the existing password re-login is used as fallback; the re-login rewrites the envelope, and a further restart still restores without login.
func TestServiceStartFallsBackToPasswordLoginWhenPersistedSessionRevoked(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	userID := fixture.createUser(t, "alice", "alice-pw-123")
	seedSavedLink(t, db, "", fixture.URL, "alice", "alice-pw-123")

	if _, err := fixture.accounts.RevokeUserSessions(ctx, userID, ""); err != nil {
		t.Fatal(err)
	}

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "revoked persisted session did not fall back to password login", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (seed login + fallback re-login)", got)
	}
	if link := linkOf(t, service); link.LastError != "" {
		t.Fatalf("unexpected last error after fallback re-login: %q", link.LastError)
	}

	service2, _ := restartService(t, db, &probeEventRecorder{})
	startProbeService(t, service2)
	waitForCondition(t, "re-persisted session not restored after second restart", func() bool {
		status, err := service2.Status(ctx)
		return err == nil && status.LoggedIn
	})
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (second restart used the re-persisted session)", got)
	}
}

// An MFA account persists the session after one interactive onboarding; after a restart the persisted session auto-connects with no /auth/login and no MFA challenge.
func TestServiceStartRestoresPersistedSessionForMFAAccount(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	totpSecret := fixture.bindTOTP(t, "alice", "alice-pw-123")

	seedVault := vault.Load(ctx, db)
	if err := seedVault.InitMaster(ctx, ""); err != nil {
		t.Fatal(err)
	}
	seed := New(db, seedVault, WithMetadata("test", true))
	result, err := seed.LinkSet(ctx, linkPatch(fixture.URL, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectMFARequired {
		t.Fatalf("connect = %q, want mfa_required", result.Connect)
	}
	if _, err := seed.LinkMFA(ctx, testTOTPCode(t, totpSecret, time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.SettingGet(ctx, settingLinkSession); err != nil || !found {
		t.Fatalf("persisted session missing after MFA onboarding: found=%v, %v", found, err)
	}

	events := &probeEventRecorder{}
	service, _ := restartService(t, db, events)
	startProbeService(t, service)

	waitForCondition(t, "MFA account not auto-connected from persisted session", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (restart skipped both login and MFA challenge)", got)
	}
	if link := linkOf(t, service); link.LastError != "" {
		t.Fatalf("unexpected last error: %q", link.LastError)
	}
}

// A locked master-password vault cannot read the persisted session envelope: probe and background rounds keep silently skipping with no events, no recorded errors, and no login attempt.
func TestServiceStartSkipsPersistedSessionWhileVaultLocked(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	seedSavedLink(t, db, "master-pw-123", fixture.URL, "alice", "alice-pw-123")

	if _, found, err := db.SettingGet(ctx, settingLinkSession); err != nil || !found {
		t.Fatalf("persisted session missing after seed: found=%v, %v", found, err)
	}
	events := &probeEventRecorder{}
	service, reloaded := restartService(t, db, events)
	if reloaded.Status().Unlocked {
		t.Fatal("master-password vault must stay locked after reload")
	}
	startProbeService(t, service)

	// Same deterministic completion signal as above: wait until the background round provably ran while locked.
	waitForCondition(t, "background round did not record a blocked attempt while the vault is locked", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.Paused && status.VaultLocked && !status.LastAttemptAt.IsZero()
	})
	if events.count() != 0 {
		t.Fatal("probe must stay silent while the vault is locked")
	}
	if status, err := service.Status(ctx); err == nil && status.LoggedIn {
		t.Fatal("logged in while the vault is locked")
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (no login attempt while the vault is locked)", got)
	}
	if link, err := service.LinkGet(context.Background()); err == nil || link.LastError != "" {
		t.Fatalf("locked vault must not record probe state: %+v, %v", link, err)
	}
}

// A proactive password change clears the persisted session (the in-memory session stays, and the server keeps the current session); the saved link password is updated too,
// and the next start falls back to re-login with the new password and re-persists.
func TestPersistedSessionClearedOnPasswordChange(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	seedSavedLink(t, db, "", fixture.URL, "alice", "alice-pw-123")

	service, _ := restartService(t, db, &probeEventRecorder{})
	startProbeService(t, service)
	waitForCondition(t, "persisted session not restored", func() bool {
		status, err := service.Status(ctx)
		return err == nil && status.LoggedIn
	})

	if _, err := service.Remote().ChangeMyPassword(ctx, "alice-pw-123", "alice-new-pw-456"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.SettingGet(ctx, settingLinkSession); err != nil || found {
		t.Fatalf("persisted session must be cleared after password change: found=%v, %v", found, err)
	}

	service2, _ := restartService(t, db, &probeEventRecorder{})
	startProbeService(t, service2)
	var lastStatus any
	var lastErr error
	waitForConditionTimeout(t, "re-login with the new password failed", 15*time.Second, func() string {
		return fmt.Sprintf("status=%+v err=%v logins=%d", lastStatus, lastErr, fixture.logins.Load())
	}, func() bool {
		status, err := service2.Status(ctx)
		lastStatus, lastErr = status, err
		return err == nil && status.LoggedIn
	})
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (seed login + re-login with the new password)", got)
	}
}

// Reconfiguring the link identity (URL/username) logs the old account out: the persisted session is cleared immediately, regardless of probe outcome.
func TestPersistedSessionClearedOnLinkIdentityChange(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	seedSavedLink(t, db, "", fixture.URL, "alice", "alice-pw-123")

	service, _ := restartService(t, db, &probeEventRecorder{})
	if _, err := service.LinkSet(ctx, LinkPatch{Username: testPtr("bob"), Password: testPtr("bob-pw-123")}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.SettingGet(ctx, settingLinkSession); err != nil || found {
		t.Fatalf("persisted session must be cleared when the linked account changes: found=%v, %v", found, err)
	}
}

// An admin reset revokes all sessions: the restart probe clears the stale envelope and falls back to password re-login (reset_required recorded truthfully);
// after forced-password-change onboarding completes, the new session and new DEK are persisted again, and a further restart restores without login.
func TestPersistedSessionReplacedOnLinkResetPassword(t *testing.T) {
	ctx := context.Background()
	db := openLinkStore(t)
	fixture := newRemoteAPIFixture(t)
	userID := fixture.createUser(t, "alice", "temp-pw-123")
	seedSavedLink(t, db, "", fixture.URL, "alice", "temp-pw-123")
	before, found, err := db.SettingGet(ctx, settingLinkSession)
	if err != nil || !found {
		t.Fatalf("persisted session missing after seed: found=%v, %v", found, err)
	}

	if err := fixture.accounts.AdminResetUser(ctx, userID); err != nil {
		t.Fatal(err)
	}

	service, _ := restartService(t, db, &probeEventRecorder{})
	startProbeService(t, service)
	waitForCondition(t, "reset-required account not flagged after server-side revocation", func() bool {
		link, err := service.LinkGet(context.Background())
		return err == nil && link.LastError == errReloginReset.Error()
	})
	if _, found, err := db.SettingGet(ctx, settingLinkSession); err != nil || found {
		t.Fatalf("persisted session must be cleared after server-side revocation: found=%v, %v", found, err)
	}

	if _, err := service.LinkResetPassword(ctx, "brand-new-pw-456"); err != nil {
		t.Fatal(err)
	}
	after, found, err := db.SettingGet(ctx, settingLinkSession)
	if err != nil || !found {
		t.Fatalf("persisted session missing after password reset: found=%v, %v", found, err)
	}
	if after == before {
		t.Fatal("persisted session must be rewritten with the post-reset session and DEK")
	}

	loginsBefore := fixture.logins.Load()
	service2, _ := restartService(t, db, &probeEventRecorder{})
	startProbeService(t, service2)
	waitForCondition(t, "post-reset persisted session not restored", func() bool {
		status, err := service2.Status(ctx)
		return err == nil && status.LoggedIn
	})
	if got := fixture.logins.Load(); got != loginsBefore {
		t.Fatalf("logins = %d, want %d (restart after reset restored the persisted session)", got, loginsBefore)
	}
}

// GT-07 ratchet: the sync engine/service test files must not reintroduce blind time.Sleep waits.
// Synchronization must use completion signals or condition polling (waitForCondition/waitForConditionTimeout);
// the only tolerated time.Sleep is the single bounded poll interval that is the last statement of
// waitForConditionTimeout's `for !condition()` loop. Any other sleep — including an extra sleep inside
// that helper — is reported.
func TestSyncScopedTestsNoBlindSleeps(t *testing.T) {
	files := []string{"engine_cursor_test.go", "engine_sync_test.go", "service_start_test.go", "settings_sync_test.go"}
	for _, name := range files {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var exempt token.Pos
			if fn.Name.Name == "waitForConditionTimeout" {
				exempt = pollSleepPos(fn)
			}
			var sleeps []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isTimeSleepCall(call) || call.Pos() == exempt {
					return true
				}
				sleeps = append(sleeps, fmt.Sprintf("line %d", fset.Position(call.Pos()).Line))
				return true
			})
			if len(sleeps) > 0 {
				t.Errorf("%s: %s uses blind time.Sleep (%s); use waitForCondition or another completion signal", name, fn.Name.Name, strings.Join(sleeps, ", "))
			}
		}
	}
}

// pollSleepPos returns the position of the one bounded poll sleep tolerated inside
// waitForConditionTimeout: the final statement of its `for !condition()` poll loop.
// It returns token.NoPos when the helper no longer has that exact shape, so every
// sleep in the helper is reported instead.
func pollSleepPos(fn *ast.FuncDecl) token.Pos {
	for _, stmt := range fn.Body.List {
		loop, ok := stmt.(*ast.ForStmt)
		if !ok {
			continue
		}
		unary, ok := loop.Cond.(*ast.UnaryExpr)
		if !ok || unary.Op != token.NOT {
			continue
		}
		condCall, ok := unary.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if ident, ok := condCall.Fun.(*ast.Ident); !ok || ident.Name != "condition" {
			continue
		}
		body := loop.Body.List
		if len(body) == 0 {
			continue
		}
		exprStmt, ok := body[len(body)-1].(*ast.ExprStmt)
		if !ok {
			continue
		}
		if call, ok := exprStmt.X.(*ast.CallExpr); ok && isTimeSleepCall(call) {
			return call.Pos()
		}
	}
	return token.NoPos
}

func isTimeSleepCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Sleep" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "time"
}
