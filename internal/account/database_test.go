package account

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func testDatabaseAccounts(t *testing.T) (*Accounts, *int64) {
	t.Helper()
	db := dbtest.NewFixture(t).OpenStore(t)
	now := int64(1_700_000_000_000)
	return New(db.GORM(), WithNow(func() int64 { return now })), &now
}

func TestDatabaseSessionMetadataListAndRevoke(t *testing.T) {
	a, now := testDatabaseAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "session-db-user", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	token, issued, err := a.IssueSessionWithMetadata(ctx, user.ID, "", SessionMetadata{
		ClientName: "NexTerm 桌面端", Origin: "192.0.2.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	*now += sessionSlidePersist.Milliseconds()
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	listed, err := a.ListUserSessions(ctx, user.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != issued.ID || listed[0].ClientName != "NexTerm 桌面端" || listed[0].Origin != "192.0.2.10" || listed[0].LastSeenAt != *now {
		t.Fatalf("session metadata after validated activity = %+v, %v", listed, err)
	}
	if err := a.RevokeUserSession(ctx, user.ID, issued.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	listed, err = a.ListUserSessions(ctx, user.ID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("revoked session still listed: %+v, %v", listed, err)
	}
}

func TestDatabaseAccountCoreFlows(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()

	alice, err := a.CreateUser(ctx, "Alice", "Alice A", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if alice.Role != RoleUser || alice.State != StateActive || alice.MustChangePassword {
		t.Fatalf("unexpected user: %+v", alice)
	}
	if _, err := a.CreateUser(ctx, "alice", "", "synthetic-password-2"); err == nil {
		t.Fatal("case-insensitive duplicate username accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if _, err := a.GetUserByUsername(ctx, "ALICE"); err != nil {
		t.Fatalf("case-insensitive lookup failed: %v", err)
	}
	if _, err := a.Authenticate(ctx, "aLiCe", "synthetic-password-1"); err != nil {
		t.Fatalf("case-insensitive login failed: %v", err)
	}
	loaded, err := a.GetUser(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastLoginAt == 0 {
		t.Fatal("last_login_at not stamped")
	}

	token, session, err := a.IssueSession(ctx, alice.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := a.ValidateSession(ctx, token)
	if err != nil || identity.UserID != alice.ID || identity.SessionID != session.ID {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}

	_, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.InsertUserDEKEnvelopes(ctx, alice.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	if err := a.InsertUserDEKEnvelopes(ctx, alice.ID, testEnvelopes(t)); err == nil || !errors.Is(err, ErrDEKEnvelopesExist) {
		t.Fatalf("second envelope upload must conflict: %v", err)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, alice.ID)
	if err != nil || !bytes.Equal(stored.KDFSalt, envelopes.KDFSalt) {
		t.Fatalf("stored envelopes mismatch: %v", err)
	}

	updated := rewrapForTest(t, "synthetic-password-1", "synthetic-password-2", envelopes)
	requireCode(t, a.ChangePassword(ctx, alice.ID, "wrong-old", "synthetic-password-2", updated, ""), ipc.CodeForbidden)
	if err := a.ChangePassword(ctx, alice.ID, "synthetic-password-1", "synthetic-password-2", updated, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatalf("kept session revoked: %v", err)
	}

	device, err := a.RegisterDevice(ctx, alice.ID, "workstation", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	deviceToken, _, err := a.IssueSession(ctx, alice.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := a.CreateUser(ctx, "bob", "", "synthetic-password-3")
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, a.RevokeDevice(ctx, bob.ID, device.ID), ipc.CodeForbidden)
	if err := a.RevokeDevice(ctx, alice.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, validateErr(a, ctx, deviceToken), ipc.CodeForbidden)
	requireCode(t, issueErr(a, ctx, alice.ID, device.ID), ipc.CodeForbidden)

	if enabled, err := a.RegistrationEnabled(ctx); err != nil || enabled {
		t.Fatalf("default registration=%v err=%v", enabled, err)
	}
	if err := a.SetRegistrationEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := a.RegistrationEnabled(ctx); err != nil || !enabled {
		t.Fatalf("after open=%v err=%v", enabled, err)
	}

	if count, err := a.CountUsers(ctx); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	users, err := a.ListUsers(ctx)
	if err != nil || len(users) != 2 || users[0].Username != "Alice" || users[1].Username != "bob" {
		t.Fatalf("users=%v err=%v", users, err)
	}

	if err := a.AdminResetUser(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	afterReset, err := a.GetUser(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReset.State != StateResetRequired || !afterReset.MustChangePassword {
		t.Fatalf("unexpected state after admin reset: %+v", afterReset)
	}
	bobEnvelopes := testEnvelopes(t)
	if err := a.ChangePassword(ctx, bob.ID, "synthetic-password-3", "synthetic-password-4", bobEnvelopes, ""); err != nil {
		t.Fatal(err)
	}
	restoredUser, err := a.GetUser(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredUser.State != StateActive || restoredUser.MustChangePassword {
		t.Fatalf("state not restored: %+v", restoredUser)
	}

	rotated := rotatedEnvelopesForTest(t, recoveryKey, "synthetic-password-5", envelopes)
	if err := a.ResetPasswordWithRecovery(ctx, "alice", vault.FormatRecoveryKey(recoveryKey), "synthetic-password-5", rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "alice", "synthetic-password-5"); err != nil {
		t.Fatalf("login with recovered password: %v", err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
}

func TestDatabaseInitSuperadminSingleUse(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := a.InitSuperadminWithEnvelopes(ctx, code, "root", "synthetic-password-1", testEnvelopes(t))
	if err != nil {
		t.Fatal(err)
	}
	if admin.Role != RoleSuperadmin {
		t.Fatalf("role=%s", admin.Role)
	}
	requireCode(t, initErr(a, ctx, code, "root2", "synthetic-password-2"), ipc.CodeForbidden)
	if _, err := a.GetUserByUsername(ctx, "root2"); err == nil {
		t.Fatal("second superadmin created with consumed code")
	}
	if _, err := a.GenerateInitCode(ctx); err == nil {
		t.Fatal("init code regenerated after consumption")
	}
}

func TestDatabaseConcurrentSessionIssueRevoke(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "uma", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, user.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	const issuers = 4
	var mu sync.Mutex
	var tokens []string
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < issuers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				token, _, err := a.IssueSession(ctx, user.ID, device.ID)
				if err != nil {
					return
				}
				mu.Lock()
				tokens = append(tokens, token)
				mu.Unlock()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if err := a.RevokeDevice(ctx, user.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	mu.Lock()
	issued := append([]string(nil), tokens...)
	mu.Unlock()
	if len(issued) == 0 {
		t.Fatal("no sessions issued during concurrency window")
	}
	for _, token := range issued {
		if _, err := a.ValidateSession(ctx, token); err == nil {
			t.Fatal("session outlived device revocation")
		}
	}
	if _, _, err := a.IssueSession(ctx, user.ID, device.ID); err == nil {
		t.Fatal("issue after revoke accepted")
	}
}

func TestDatabaseConcurrentChangePasswordSameOld(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "wendy", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	passwords := []string{"new-password-a", "new-password-b"}
	updated := []*vault.UserDEKEnvelopes{
		rewrapForTest(t, "old-password-1", passwords[0], envelopes),
		rewrapForTest(t, "old-password-1", passwords[1], envelopes),
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range passwords {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = a.ChangePassword(ctx, user.ID, "old-password-1", passwords[i], updated[i], "")
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("both password changes succeeded: %v", errs)
			}
			winner = i
			continue
		}
		requireCode(t, err, ipc.CodeForbidden)
	}
	if winner == -1 {
		t.Fatalf("no password change succeeded: %v", errs)
	}
	requireCode(t, authErr(a, ctx, "wendy", "old-password-1"), ipc.CodeForbidden)
	if _, err := a.Authenticate(ctx, "wendy", passwords[winner]); err != nil {
		t.Fatalf("winner password rejected: %v", err)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.DEKEnvelope, updated[winner].DEKEnvelope) {
		t.Fatal("stored envelope does not match the winning password change")
	}
}

func TestDatabaseConcurrentRecoveryReset(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "xavier", "", "old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes("old-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
	passwords := []string{"new-password-a", "new-password-b"}
	rotated := []*vault.UserDEKEnvelopes{
		rotatedEnvelopesForTest(t, recoveryKey, passwords[0], envelopes),
		rotatedEnvelopesForTest(t, recoveryKey, passwords[1], envelopes),
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range passwords {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = a.ResetPasswordWithRecovery(ctx, "xavier", vault.FormatRecoveryKey(recoveryKey), passwords[i], rotated[i])
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("both recovery resets succeeded: %v", errs)
			}
			winner = i
			continue
		}
		requireCode(t, err, ipc.CodeForbidden)
	}
	if winner == -1 {
		t.Fatalf("no recovery reset succeeded: %v", errs)
	}
	if _, err := a.Authenticate(ctx, "xavier", passwords[winner]); err != nil {
		t.Fatalf("winner password rejected: %v", err)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.RecoveryEnvelope, rotated[winner].RecoveryEnvelope) {
		t.Fatal("stored recovery envelope does not match the winning reset")
	}
}

func TestDatabaseConcurrentDeviceRevoke(t *testing.T) {
	a, _ := testDatabaseAccounts(t)
	ctx := context.Background()
	owner, err := a.CreateUser(ctx, "kate", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, owner.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, owner.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	const revokers = 4
	var wg sync.WaitGroup
	errs := make([]error, revokers)
	for i := 0; i < revokers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = a.RevokeDevice(ctx, owner.ID, device.ID)
		}(i)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		requireCode(t, err, ipc.CodeForbidden)
	}
	if succeeded != 1 {
		t.Fatalf("concurrent device revoke succeeded %d times, want exactly 1", succeeded)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
}
