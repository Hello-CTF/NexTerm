package account

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestDeviceLifecycleAndRevokeCascade(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	owner, err := a.CreateUser(ctx, "ivy", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.CreateUser(ctx, "jack", "", "synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, owner.ID, "workstation", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.RegisterDevice(ctx, owner.ID, "", "desktop"); err == nil {
		t.Fatal("empty device name accepted")
	}
	if _, err := a.RegisterDevice(ctx, owner.ID, "phone", "mobile"); err != nil {
		t.Fatal(err)
	}
	devices, err := a.ListDevices(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("device count=%d", len(devices))
	}
	if err := a.TouchDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastSeenAt == 0 || loaded.UserID != owner.ID {
		t.Fatalf("unexpected device: %+v", loaded)
	}

	token, _, err := a.IssueSession(ctx, owner.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at)
VALUES('sc1', ?, ?, 'sync', 'synthetic-hash', 1)`, owner.ID, device.ID); err != nil {
		t.Fatal(err)
	}

	requireCode(t, a.RevokeDevice(ctx, other.ID, device.ID), ipc.CodeForbidden)
	if err := a.RevokeDevice(ctx, owner.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, a.RevokeDevice(ctx, owner.ID, device.ID), ipc.CodeForbidden)
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	var credRevoked int64
	if err := a.db.QueryRowContext(ctx, "SELECT revoked_at FROM sync_credential WHERE id = 'sc1'").Scan(&credRevoked); err != nil {
		t.Fatal(err)
	}
	if credRevoked == 0 {
		t.Fatal("sync credential not revoked with device")
	}
}

func TestEnrollCodeOneTimeLifecycle(t *testing.T) {
	a, now := testAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "kate", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	code, err := a.IssueEnrollCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if code == "" {
		t.Fatal("empty enroll code")
	}
	consumedBy, err := a.ConsumeEnrollCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if consumedBy != user.ID {
		t.Fatalf("consumed by %q, want %q", consumedBy, user.ID)
	}
	requireCode(t, consumeCodeErr(a, ctx, code), ipc.CodeForbidden)
	requireCode(t, consumeCodeErr(a, ctx, "not-a-code"), ipc.CodeForbidden)

	expiring, err := a.IssueEnrollCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	*now += EnrollCodeTTL.Milliseconds() + 1
	requireCode(t, consumeCodeErr(a, ctx, expiring), ipc.CodeForbidden)
}

func consumeCodeErr(a *Accounts, ctx context.Context, code string) error {
	_, err := a.ConsumeEnrollCode(ctx, code)
	return err
}
