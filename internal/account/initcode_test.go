package account

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func TestInitSuperadminRandomPasswordFirstLoginChange(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, password, err := a.InitSuperadminWithPolicy(ctx, code, "root", "", nil, InitialPasswordRandom)
	if err != nil {
		t.Fatal(err)
	}
	if password == "" {
		t.Fatal("random policy must hand the generated password to the installer")
	}
	if len(password) < MinPasswordLength {
		t.Fatalf("generated password shorter than %d: %d", MinPasswordLength, len(password))
	}
	if admin.Role != RoleSuperadmin || admin.State != StateResetRequired || !admin.MustChangePassword {
		t.Fatalf("unexpected admin: %+v", admin)
	}
	// The init code stays single-use under the random policy.
	if _, reused, err := a.InitSuperadminWithPolicy(ctx, code, "root2", "", nil, InitialPasswordRandom); err == nil {
		t.Fatal("consumed init code accepted")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
		if reused != "" {
			t.Fatal("failed init must not return a password")
		}
	}
	// Only the hash is stored; the plaintext never reaches the database.
	stored, err := a.GetUserByUsername(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored.PasswordHash(), "$argon2id$") {
		t.Fatalf("stored password is not an argon2id hash: %q", stored.PasswordHash())
	}
	if strings.Contains(stored.PasswordHash(), password) {
		t.Fatal("plaintext password stored")
	}
	// First login succeeds and carries the forced-change state to the caller.
	session, err := a.Authenticate(ctx, "root", password)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != StateResetRequired || !session.MustChangePassword {
		t.Fatalf("first login state: %+v", session)
	}
	// Changing the password clears the forced-change state.
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		t.Fatal(err)
	}
	updated := rewrapForTest(t, password, "synthetic-password-2", envelopes)
	if err := a.ChangePassword(ctx, admin.ID, password, "synthetic-password-2", updated, ""); err != nil {
		t.Fatal(err)
	}
	after, err := a.GetUser(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != StateActive || after.MustChangePassword {
		t.Fatalf("forced-change state not cleared: %+v", after)
	}
	if _, err := a.Authenticate(ctx, "root", "synthetic-password-2"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	requireCode(t, authErr(a, ctx, "root", password), ipc.CodeForbidden)
}

func TestInitSuperadminSuppliedPasswordNoForcedRotation(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, password, err := a.InitSuperadminWithPolicy(ctx, code, "root", "synthetic-password-1", nil, InitialPasswordSupplied)
	if err != nil {
		t.Fatal(err)
	}
	if password != "" {
		t.Fatal("supplied policy must not return a password")
	}
	if admin.State != StateActive || admin.MustChangePassword {
		t.Fatalf("supplied policy must not force rotation: %+v", admin)
	}
	session, err := a.Authenticate(ctx, "root", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if session.State != StateActive || session.MustChangePassword {
		t.Fatalf("supplied policy login state: %+v", session)
	}
}

func TestInitSuperadminRandomPasswordWithEnvelopes(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	envelopes := testEnvelopes(t)
	admin, password, err := a.InitSuperadminWithPolicy(ctx, code, "root", "", envelopes, InitialPasswordRandom)
	if err != nil {
		t.Fatal(err)
	}
	if password == "" || admin.MustChangePassword != true || admin.State != StateResetRequired {
		t.Fatalf("unexpected admin: %+v", admin)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, admin.ID)
	if err != nil {
		t.Fatalf("envelope was not written with the account: %v", err)
	}
	if stored.KDFParams != envelopes.KDFParams || stored.RecoveryHash != envelopes.RecoveryHash {
		t.Fatal("stored envelopes mismatch")
	}
}

func TestInitSuperadminRandomPasswordRetryAfterFailure(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failDEKInsertTrigger(t, a)
	failed, password, err := a.InitSuperadminWithPolicy(ctx, code, "root", "", testEnvelopes(t), InitialPasswordRandom)
	if err == nil {
		t.Fatal("envelope write failure must fail initialization")
	}
	if failed != nil || password != "" {
		t.Fatal("failed init must not return an account or password")
	}
	dropFailDEKInsertTrigger(t, a)
	count, err := a.CountUsers(ctx)
	if err != nil || count != 0 {
		t.Fatalf("failure must not leave a partial account: count=%d err=%v", count, err)
	}
	// The init code remains unconsumed and the retry issues a fresh password.
	admin, retryPassword, err := a.InitSuperadminWithPolicy(ctx, code, "root", "", testEnvelopes(t), InitialPasswordRandom)
	if err != nil {
		t.Fatalf("initialization code must remain unconsumed: %v", err)
	}
	if retryPassword == "" || admin.State != StateResetRequired {
		t.Fatalf("unexpected admin: %+v", admin)
	}
	if _, err := a.Authenticate(ctx, "root", retryPassword); err != nil {
		t.Fatalf("retry password rejected: %v", err)
	}
}

func TestInitSuperadminRandomPasswordRefusedWhenUsersExist(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	if _, err := a.CreateUser(ctx, "existing", "", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, password, err := a.InitSuperadminWithPolicy(ctx, code, "root", "", nil, InitialPasswordRandom)
	requireCode(t, err, ipc.CodeForbidden)
	if admin != nil || password != "" {
		t.Fatal("refused init must not return an account or password")
	}
	if strings.Contains(err.Error(), code) {
		t.Fatal("init code leaked into error")
	}
	if _, err := a.GetUserByUsername(ctx, "root"); err == nil {
		t.Fatal("superadmin created over existing users")
	}
}

func TestInitSuperadminRandomPasswordWrongCode(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, password, err := a.InitSuperadminWithPolicy(ctx, "wrong-code", "root", "", nil, InitialPasswordRandom)
	requireCode(t, err, ipc.CodeForbidden)
	if admin != nil || password != "" {
		t.Fatal("failed init must not return an account or password")
	}
	if strings.Contains(err.Error(), code) {
		t.Fatal("init code leaked into error")
	}
	if _, err := a.GetUserByUsername(ctx, "root"); err == nil {
		t.Fatal("user created despite wrong init code")
	}
	// The code stays usable after a wrong attempt.
	admin, password, err = a.InitSuperadminWithPolicy(ctx, code, "root", "", nil, InitialPasswordRandom)
	if err != nil || admin == nil || password == "" {
		t.Fatalf("init with correct code after failure: admin=%v err=%v", admin, err)
	}
}

func TestInitSuperadminUnknownPolicyRefused(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, password, err := a.InitSuperadminWithPolicy(ctx, code, "root", "synthetic-password-1", nil, InitialPasswordPolicy(99))
	requireCode(t, err, ipc.CodeBadParam)
	if admin != nil || password != "" {
		t.Fatal("refused init must not return an account or password")
	}
	if _, err := a.GetUserByUsername(ctx, "root"); err == nil {
		t.Fatal("user created under unknown policy")
	}
	// The code stays unconsumed for a valid policy.
	if _, _, err := a.InitSuperadminWithPolicy(ctx, code, "root", "synthetic-password-1", nil, InitialPasswordSupplied); err != nil {
		t.Fatalf("init with valid policy after refusal: %v", err)
	}
}

func failSettingDeleteTrigger(t *testing.T, a *Accounts) {
	t.Helper()
	if err := a.db.Exec(`CREATE TRIGGER fail_setting_delete BEFORE DELETE ON setting
BEGIN
	SELECT RAISE(ABORT, 'injected setting delete failure');
END`).Error; err != nil {
		t.Fatal(err)
	}
}

func dropFailSettingDeleteTrigger(t *testing.T, a *Accounts) {
	t.Helper()
	if err := a.db.Exec("DROP TRIGGER fail_setting_delete").Error; err != nil {
		t.Fatal(err)
	}
}

func TestReleaseInitCodeDeletesOwnUnconsumedCode(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ReleaseInitCode(ctx, code); err != nil {
		t.Fatalf("release of own unconsumed code: %v", err)
	}
	// The released code is gone: initialization with it fails and a fresh
	// code can be generated again.
	requireCode(t, func() error {
		_, err := a.InitSuperadmin(ctx, code, "root", "synthetic-password-1")
		return err
	}(), ipc.CodeForbidden)
	fresh, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatalf("a released code must free the slot for a fresh one: %v", err)
	}
	if fresh == code {
		t.Fatal("expected a freshly generated code")
	}
}

func TestReleaseInitCodeRefusesForeignPlaintext(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A plaintext that does not match the stored hash proves no ownership:
	// the record must survive untouched.
	requireCode(t, a.ReleaseInitCode(ctx, "not-the-code"), ipc.CodeForbidden)
	if _, err := a.InitSuperadmin(ctx, code, "root", "synthetic-password-1"); err != nil {
		t.Fatalf("code unusable after a foreign release attempt: %v", err)
	}
}

func TestReleaseInitCodeRefusesConsumedCode(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.InitSuperadmin(ctx, code, "root", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, a.ReleaseInitCode(ctx, code), ipc.CodeForbidden)
}

func TestReleaseInitCodeNoopWithoutStoredCode(t *testing.T) {
	a, _ := testAccounts(t)
	if err := a.ReleaseInitCode(context.Background(), "anything"); err != nil {
		t.Fatalf("release without a stored code must be a no-op: %v", err)
	}
}

func TestReleaseInitCodeCleanupFailureThenRetry(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The failed create and the cleanup both fail: the unconsumed record the
	// installer owns stays behind.
	failSettingDeleteTrigger(t, a)
	if _, _, err := a.InitSuperadminWithPolicy(ctx, code, "root", "short", nil, InitialPasswordSupplied); err == nil {
		t.Fatal("invalid password must fail creation")
	}
	if err := a.ReleaseInitCode(ctx, code); err == nil {
		t.Fatal("injected delete failure must fail the release")
	}
	dropFailSettingDeleteTrigger(t, a)
	// Once the fault is gone the retry releases and the slot frees up.
	if err := a.ReleaseInitCode(ctx, code); err != nil {
		t.Fatalf("retry release after the fault cleared: %v", err)
	}
	if _, err := a.GenerateInitCode(ctx); err != nil {
		t.Fatalf("generate after a successful cleanup: %v", err)
	}
}
