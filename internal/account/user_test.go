package account

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func requireCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected code %s, got %v", code, err)
	}
}

func TestPasswordHashVerify(t *testing.T) {
	hash, err := hashPassword("synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected phc format: %q", hash)
	}
	if !verifyPassword(hash, "synthetic-password-1") {
		t.Fatal("verify failed")
	}
	if verifyPassword(hash, "wrong-password") {
		t.Fatal("verify accepted wrong password")
	}
	if verifyPassword("not-a-phc-string", "synthetic-password-1") {
		t.Fatal("verify accepted malformed hash")
	}
	if verifyPassword("$argon2id$v=19$m=99999999,t=3,p=4$AAAA$AAAA", "x") {
		t.Fatal("verify accepted out-of-range params")
	}
}

func TestCreateUserIsOrdinaryAndUnique(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "Alice", "Alice A", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != RoleUser || user.State != StateActive || user.MustChangePassword {
		t.Fatalf("unexpected user: %+v", user)
	}
	if _, err := a.CreateUser(ctx, "alice", "", "synthetic-password-2"); err == nil {
		t.Fatal("NOCASE duplicate username accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if _, err := a.CreateUser(ctx, "bob", "", "short"); err == nil {
		t.Fatal("short password accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if _, err := a.CreateUser(ctx, "bad name", "", "synthetic-password-2"); err == nil {
		t.Fatal("username with space accepted")
	}
}

func TestAuthenticate(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "carol", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "carol", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, authErr(a, ctx, "carol", "wrong-password"), ipc.CodeForbidden)
	requireCode(t, authErr(a, ctx, "nobody", "synthetic-password-1"), ipc.CodeForbidden)
	if _, err := a.GetUserByUsername(ctx, "CAROL"); err != nil {
		t.Fatal("NOCASE lookup failed")
	}
	if _, err := a.Authenticate(ctx, "carol", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastLoginAt == 0 {
		t.Fatal("last_login_at not stamped")
	}
	if _, err := a.db.ExecContext(ctx, "UPDATE app_user SET state = ? WHERE id = ?", string(StateDisabled), user.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, authErr(a, ctx, "carol", "synthetic-password-1"), ipc.CodeForbidden)
}

func authErr(a *Accounts, ctx context.Context, username, password string) error {
	_, err := a.Authenticate(ctx, username, password)
	return err
}

func TestInitSuperadminSingleUse(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" {
		t.Fatal("empty init code")
	}
	if _, err := a.GenerateInitCode(ctx); err == nil {
		t.Fatal("second init code generation accepted")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	requireCode(t, initErr(a, ctx, "wrong-code", "root", "synthetic-password-1"), ipc.CodeForbidden)
	if _, err := a.GetUserByUsername(ctx, "root"); err == nil {
		t.Fatal("user created despite wrong init code")
	}
	admin, err := a.InitSuperadmin(ctx, code, "root", "synthetic-password-1")
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

func TestInitSuperadminRefusedWhenUsersExist(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	if _, err := a.CreateUser(ctx, "existing", "", "synthetic-password-1"); err != nil {
		t.Fatal(err)
	}
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, initErr(a, ctx, code, "root", "synthetic-password-2"), ipc.CodeForbidden)
	if _, err := a.GetUserByUsername(ctx, "root"); err == nil {
		t.Fatal("superadmin created over existing users")
	}
}

func initErr(a *Accounts, ctx context.Context, code, username, password string) error {
	_, err := a.InitSuperadmin(ctx, code, username, password)
	return err
}
