package account

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	if err := a.db.Exec(`INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at)
VALUES('sc1', ?, ?, 'sync', 'synthetic-hash', 1)`, owner.ID, device.ID).Error; err != nil {
		t.Fatal(err)
	}

	requireCode(t, a.RevokeDevice(ctx, other.ID, device.ID), ipc.CodeForbidden)
	if err := a.RevokeDevice(ctx, owner.ID, device.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, a.RevokeDevice(ctx, owner.ID, device.ID), ipc.CodeForbidden)
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
	var credRevoked int64
	if err := a.db.Raw("SELECT revoked_at FROM sync_credential WHERE id = 'sc1'").Row().Scan(&credRevoked); err != nil {
		t.Fatal(err)
	}
	if credRevoked == 0 {
		t.Fatal("sync credential not revoked with device")
	}
}
