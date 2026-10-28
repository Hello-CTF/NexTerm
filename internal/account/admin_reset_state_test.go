package account

import (
	"context"
	"testing"
)

func TestAdminResetKeepsDisabledAccountDisabled(t *testing.T) {
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
	if got.State != StateDisabled {
		t.Fatalf("admin reset must not re-enable a disabled account, state=%s", got.State)
	}
	if err := a.SetUserDisabled(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err = a.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateResetRequired {
		t.Fatalf("re-enabling a reset-pending account must keep the reset requirement, state=%s", got.State)
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
