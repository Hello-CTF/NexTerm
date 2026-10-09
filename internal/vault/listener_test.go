package vault

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestUnlockListenerFiresOnUnlockPaths(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	fired := 0
	v.AddUnlockListener(func() { fired++ })
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("InitMaster fired %d listeners, want 1", fired)
	}
	v.Lock()
	if fired != 1 {
		t.Fatal("Lock must not fire unlock listeners")
	}
	requireVaultCode(t, v.UnlockMaster(ctx, "wrong-password"), ipc.CodeBadMasterPass)
	if fired != 1 {
		t.Fatal("failed unlock must not fire listeners")
	}
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("UnlockMaster fired %d listeners, want 2", fired)
	}
}

func TestUnlockListenerFiresOnDPAPIInit(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	fired := 0
	v.AddUnlockListener(func() { fired++ })
	if err := v.InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("InitDPAPI fired %d listeners, want 1", fired)
	}
}
