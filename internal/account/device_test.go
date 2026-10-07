package account

import (
	"context"
	"sync"
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

func TestRevokeDeviceConcurrentSingleWinner(t *testing.T) {
	a := testFileAccounts(t)
	ctx := context.Background()
	owner, err := a.CreateUser(ctx, "kate2", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.RegisterDevice(ctx, owner.ID, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.IssueSession(ctx, owner.ID, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	const revokers = 4
	var wg sync.WaitGroup
	errs := make([]error, revokers)
	for i := 0; i < revokers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = a.RevokeDevice(ctx, owner.ID, device.ID)
		}(i)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		requireCode(t, err, ipc.CodeForbidden)
	}
	if succeeded != 1 {
		t.Fatalf("concurrent device revoke succeeded %d times, want exactly 1", succeeded)
	}
	requireCode(t, validateErr(a, ctx, token), ipc.CodeForbidden)
}

func TestConsumeEnrollCodeConcurrentSingleUse(t *testing.T) {
	a := testFileAccounts(t)
	ctx := context.Background()
	user, err := a.CreateUser(ctx, "lena2", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	code, err := a.IssueEnrollCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	const consumers = 8
	var wg sync.WaitGroup
	userIDs := make([]string, consumers)
	errs := make([]error, consumers)
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			userIDs[i], errs[i] = a.ConsumeEnrollCode(ctx, code)
		}(i)
	}
	wg.Wait()
	succeeded := 0
	for i, err := range errs {
		if err == nil {
			succeeded++
			if userIDs[i] != user.ID {
				t.Fatalf("code consumed by wrong user %q", userIDs[i])
			}
			continue
		}
		requireCode(t, err, ipc.CodeForbidden)
	}
	if succeeded != 1 {
		t.Fatalf("concurrent consume succeeded %d times, want exactly 1", succeeded)
	}
}

func TestEnrollCodeMultiUserIsolation(t *testing.T) {
	a, _ := testAccounts(t)
	ctx := context.Background()
	alice, err := a.CreateUser(ctx, "alice-enroll", "", "synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := a.CreateUser(ctx, "bob-enroll", "", "synthetic-password-2")
	if err != nil {
		t.Fatal(err)
	}
	codeA1, err := a.IssueEnrollCode(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	codeA2, err := a.IssueEnrollCode(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	codeB, err := a.IssueEnrollCode(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if consumedBy, err := a.ConsumeEnrollCode(ctx, codeB); err != nil || consumedBy != bob.ID {
		t.Fatalf("consume B: %q, %v", consumedBy, err)
	}
	if consumedBy, err := a.ConsumeEnrollCode(ctx, codeA1); err != nil || consumedBy != alice.ID {
		t.Fatalf("consume A1: %q, %v", consumedBy, err)
	}
	requireCode(t, consumeCodeErr(a, ctx, codeA1), ipc.CodeForbidden)
	if consumedBy, err := a.ConsumeEnrollCode(ctx, codeA2); err != nil || consumedBy != alice.ID {
		t.Fatalf("A2 must survive A1 consumption: %q, %v", consumedBy, err)
	}
	requireCode(t, consumeCodeErr(a, ctx, codeB), ipc.CodeForbidden)
}
