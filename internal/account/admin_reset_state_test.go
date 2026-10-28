package account

import (
	"context"
	"testing"
)

func TestAdminResetOfDisabledAccountRequiresPasswordChange(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)
	user, err := a.CreateUser(ctx, "alice", "", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateResetRequired || !got.MustChangePassword {
		t.Fatalf("an admin reset must leave a forced password change, state=%s must_change=%v", got.State, got.MustChangePassword)
	}
}

func TestEnableKeepsPendingResetRequirement(t *testing.T) {
	ctx := context.Background()
	a, _ := testAccounts(t)
	user, err := a.CreateUser(ctx, "alice", "", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserDisabled(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateResetRequired {
		t.Fatalf("disable/enable must not clear a pending reset, state=%s", got.State)
	}
}
