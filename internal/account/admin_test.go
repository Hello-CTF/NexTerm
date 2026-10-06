package account

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestCountUsersAndListUsers(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)

	count, err := a.CountUsers(ctx)
	if err != nil || count != 0 {
		t.Fatalf("empty count=%d err=%v", count, err)
	}
	if _, err := a.CreateUser(ctx, "alice", "Alice", "password-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser(ctx, "bob", "", "password-2"); err != nil {
		t.Fatal(err)
	}
	count, err = a.CountUsers(ctx)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	users, err := a.ListUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("list=%v err=%v", users, err)
	}
	if users[0].Username != "alice" || users[1].Username != "bob" {
		t.Fatalf("list order=%s,%s", users[0].Username, users[1].Username)
	}
	if users[0].PasswordHash() == "" {
		t.Fatal("ListUsers 必须返回完整账号记录")
	}
}

func TestSetUserDisabledRevokesSessions(t *testing.T) {
	ctx := context.Background()
	a, now := testAccounts(t)
	user, err := a.CreateUser(ctx, "alice", "", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateSession(ctx, token); err != nil {
		t.Fatalf("session before disable: %v", err)
	}

	if err := a.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateSession(ctx, token); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("disabled session must fail validation: %v", err)
	}
	if _, err := a.Authenticate(ctx, "alice", "password-1"); err == nil || ipc.NormalizeError(err).Message != "账号已禁用" {
		t.Fatalf("disabled login: %v", err)
	}

	if err := a.SetUserDisabled(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	*now += 1000
	if _, err := a.Authenticate(ctx, "alice", "password-1"); err != nil {
		t.Fatalf("re-enabled login: %v", err)
	}
	if _, err := a.ValidateSession(ctx, token); err == nil {
		t.Fatal("吊销的会话不得因恢复账号而复活")
	}

	if err := a.SetUserDisabled(ctx, "missing", true); err == nil || ipc.NormalizeError(err).Code != ipc.CodeNotFound {
		t.Fatalf("disable missing user: %v", err)
	}
}

func TestRegistrationEnabledDefaultClosed(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)

	enabled, err := a.RegistrationEnabled(ctx)
	if err != nil || enabled {
		t.Fatalf("default registration=%v err=%v", enabled, err)
	}
	if err := a.SetRegistrationEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := a.RegistrationEnabled(ctx); err != nil || !enabled {
		t.Fatalf("after open=%v err=%v", enabled, err)
	}
	if err := a.SetRegistrationEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := a.RegistrationEnabled(ctx); err != nil || enabled {
		t.Fatalf("after close=%v err=%v", enabled, err)
	}
}

func failDEKInsertTrigger(t *testing.T, a *Accounts) {
	t.Helper()
	if _, err := a.db.ExecContext(context.Background(), `CREATE TRIGGER fail_dek_insert BEFORE INSERT ON user_dek
BEGIN
	SELECT RAISE(ABORT, 'injected dek failure');
END`); err != nil {
		t.Fatal(err)
	}
}

func dropFailDEKInsertTrigger(t *testing.T, a *Accounts) {
	t.Helper()
	if _, err := a.db.ExecContext(context.Background(), "DROP TRIGGER fail_dek_insert"); err != nil {
		t.Fatal(err)
	}
}

func testEnvelopes(t *testing.T) *vault.UserDEKEnvelopes {
	t.Helper()
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("password-1")
	if err != nil {
		t.Fatal(err)
	}
	return envelopes
}

func TestCreateUserWithEnvelopesAtomic(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)
	failDEKInsertTrigger(t, a)
	if _, err := a.CreateUserWithEnvelopes(ctx, "alice", "", "password-1", testEnvelopes(t)); err == nil {
		t.Fatal("信封写入失败必须让创建失败")
	}
	dropFailDEKInsertTrigger(t, a)
	count, err := a.CountUsers(ctx)
	if err != nil || count != 0 {
		t.Fatalf("失败不得留下部分账号: count=%d err=%v", count, err)
	}
	user, err := a.CreateUserWithEnvelopes(ctx, "alice", "", "password-1", testEnvelopes(t))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil || len(stored.DEKEnvelope) == 0 {
		t.Fatalf("信封未随账号写入: %v", err)
	}
}

func TestInitSuperadminWithEnvelopesAtomic(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)
	code, err := a.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failDEKInsertTrigger(t, a)
	if _, err := a.InitSuperadminWithEnvelopes(ctx, code, "root", "password-1", testEnvelopes(t)); err == nil {
		t.Fatal("信封写入失败必须让初始化失败")
	}
	dropFailDEKInsertTrigger(t, a)
	count, err := a.CountUsers(ctx)
	if err != nil || count != 0 {
		t.Fatalf("失败不得留下部分账号: count=%d err=%v", count, err)
	}
	user, err := a.InitSuperadminWithEnvelopes(ctx, code, "root", "password-1", testEnvelopes(t))
	if err != nil {
		t.Fatalf("初始化码必须保持未消费: %v", err)
	}
	if user.Role != RoleSuperadmin {
		t.Fatalf("role=%s", user.Role)
	}
	if _, err := a.InitSuperadminWithEnvelopes(ctx, code, "root2", "password-2", testEnvelopes(t)); err == nil {
		t.Fatal("初始化码一次性语义被破坏")
	}
}

func TestInsertUserDEKEnvelopesOnce(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)
	user, err := a.CreateUser(ctx, "alice", "", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	first, second := testEnvelopes(t), testEnvelopes(t)
	if err := a.InsertUserDEKEnvelopes(ctx, user.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := a.InsertUserDEKEnvelopes(ctx, user.ID, second); err == nil || !errors.Is(err, ErrDEKEnvelopesExist) {
		t.Fatalf("第二次上传必须冲突: %v", err)
	}
	stored, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.KDFSalt, first.KDFSalt) {
		t.Fatal("首次上传的信封被覆盖")
	}

	done := make(chan error, 2)
	for _, envelopes := range []*vault.UserDEKEnvelopes{testEnvelopes(t), testEnvelopes(t)} {
		go func(envelopes *vault.UserDEKEnvelopes) {
			done <- a.InsertUserDEKEnvelopes(ctx, user.ID, envelopes)
		}(envelopes)
	}
	<-done
	<-done
	storedAfter, err := a.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedAfter.KDFSalt, first.KDFSalt) {
		t.Fatal("并发上传覆盖了既有信封")
	}
}
