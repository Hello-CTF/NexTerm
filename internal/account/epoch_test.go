package account

import (
	"context"
	"testing"
)

func TestAccountEpochBumpEvents(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "alice", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	epoch := a.Epoch(user.ID)
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("fresh account epoch mismatch")
	}

	// Session issuance alone does not advance the generation.
	_, first, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("session issuance bumped the epoch")
	}

	// A single-session revocation bumps it.
	if err := a.RevokeSession(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("session revoke did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// A user-scoped single-session revocation bumps it.
	_, second, err := a.IssueSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeUserSession(ctx, user.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("user session revoke did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// Revoking all sessions bumps it.
	if _, _, err := a.IssueSession(ctx, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RevokeUserSessionsWithIDs(ctx, user.ID, ""); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("revoke-all did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// A device revocation bumps it.
	device, err := a.RegisterDevice(ctx, user.ID, "alice-phone", "mobile")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeDevice(ctx, user.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("device revoke did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// Disabling bumps it; re-enabling does not.
	if err := a.SetUserDisabled(ctx, user.ID, true); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("disable did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)
	if err := a.SetUserDisabled(ctx, user.ID, false); err != nil {
		t.Fatal(err)
	}
	if !a.CheckEpoch(user.ID, epoch) {
		t.Fatal("re-enable bumped the epoch")
	}

	// A password change bumps it.
	if err := a.ChangePassword(ctx, user.ID, "password-123", "password-456", testEnvelopes(t), ""); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("password change did not bump the epoch")
	}
	epoch = a.Epoch(user.ID)

	// An admin reset bumps it.
	if err := a.AdminResetUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if a.CheckEpoch(user.ID, epoch) {
		t.Fatal("admin reset did not bump the epoch")
	}
}

func TestTOTPSetupConfirmEpochBinding(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "alice", "", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// A revocation between setup and confirm invalidates the pending binding.
	a.epochs.bump(user.ID)
	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret, *now/1000)); err == nil {
		t.Fatal("stale-generation TOTP confirm succeeded")
	}
	if enabled, _, _, err := a.TOTPStatus(ctx, user.ID); err != nil || enabled {
		t.Fatalf("stale-generation confirm enabled TOTP: %v %v", enabled, err)
	}

	// A fresh setup binds to the current generation and confirms.
	secret2, _, err := a.BeginTOTPSetup(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmTOTPSetup(ctx, user.ID, totpCodeForTest(t, secret2, *now/1000)); err != nil {
		t.Fatalf("fresh-generation TOTP confirm failed: %v", err)
	}
}
