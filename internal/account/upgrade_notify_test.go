package account

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func newUpgradeNotificationAccounts(t *testing.T) (*Accounts, *int) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	notifications := new(int)
	accounts := New(database.GORM(), WithActiveSuperadminListener(func(ctx context.Context) {
		var count int64
		if err := database.GORM().WithContext(ctx).Model(&store.AppUser{}).
			Where("role = ? AND state = ?", string(RoleSuperadmin), string(StateActive)).Count(&count).Error; err != nil {
			t.Errorf("read active superadmin from listener: %v", err)
			return
		}
		if count == 0 {
			t.Error("listener ran before the active superadmin was committed")
			return
		}
		*notifications = *notifications + 1
	}))
	return accounts, notifications
}

func TestActiveSuperadminListenerAfterInitAndReenable(t *testing.T) {
	accounts, notifications := newUpgradeNotificationAccounts(t)
	ctx := context.Background()
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := accounts.InitSuperadmin(ctx, code, "root", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("notifications after committed init = %d, want 1", *notifications)
	}
	if _, err := accounts.CreateUser(ctx, "ordinary", "", "ordinary-password-1"); err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("ordinary user creation notified %d times, want still 1", *notifications)
	}
	if err := accounts.SetUserDisabled(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("disable notified %d times, want still 1", *notifications)
	}
	if err := accounts.SetUserDisabled(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}
	if *notifications != 2 {
		t.Fatalf("notifications after reenable = %d, want 2", *notifications)
	}
}

func TestActiveSuperadminListenerWaitsForRandomPasswordActivation(t *testing.T) {
	accounts, notifications := newUpgradeNotificationAccounts(t)
	ctx := context.Background()
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, initialPassword, err := accounts.InitSuperadminWithPolicy(ctx, code, "root", "", nil, InitialPasswordRandom)
	if err != nil {
		t.Fatal(err)
	}
	if *notifications != 0 {
		t.Fatalf("reset-required admin notified %d times, want 0", *notifications)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("replacement-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.ChangePassword(ctx, admin.ID, initialPassword, "replacement-password-1", envelopes, ""); err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("notifications after activation = %d, want 1", *notifications)
	}
	_, nextEnvelopes, _, err := vault.GenerateUserDEKEnvelopes("replacement-password-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.ChangePassword(ctx, admin.ID, "replacement-password-1", "replacement-password-2", nextEnvelopes, ""); err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("ordinary active-admin password change retriggered migration: %d", *notifications)
	}
}

func TestActiveSuperadminListenerPlatformOwnerCommittedOnce(t *testing.T) {
	accounts, notifications := newUpgradeNotificationAccounts(t)
	if _, err := accounts.EnsurePlatformOwner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("notifications after platform owner creation = %d, want 1", *notifications)
	}
	if _, err := accounts.EnsurePlatformOwner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *notifications != 1 {
		t.Fatalf("existing platform owner retriggered listener: %d", *notifications)
	}
}
