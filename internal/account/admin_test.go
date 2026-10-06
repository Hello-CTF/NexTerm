package account

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
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
