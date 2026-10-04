package vault

import (
	"context"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestSetAutoLockValidation(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	requireVaultCode(t, v.SetAutoLock(ctx, 1441), ipc.CodeBadParam)
	if got := v.Status().AutoLockMinutes; got != 30 {
		t.Fatalf("rejected value must not change the setting, got %d", got)
	}
	if err := v.SetAutoLock(ctx, 1440); err != nil {
		t.Fatal(err)
	}
	if got := v.Status().AutoLockMinutes; got != 1440 {
		t.Fatalf("status = %d, want 1440", got)
	}
	if err := v.SetAutoLock(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := v.Status().AutoLockMinutes; got != 0 {
		t.Fatalf("status = %d, want 0", got)
	}
}

func TestAutoLockDisabledNeverLocks(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := int64(1000)
	v := loadTestVault(db, &fakeProtector{}, func(v *Vault) { v.now = func() int64 { return now } })
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.SetAutoLock(ctx, 0); err != nil {
		t.Fatal(err)
	}
	now += 30 * 24 * 3600_000
	v.AutoLockIfIdle()
	if !v.Status().Unlocked {
		t.Fatal("disabled autolock must not lock the vault")
	}
}

func TestAutoLockPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.SetAutoLock(ctx, 7); err != nil {
		t.Fatal(err)
	}
	raw, _, err := db.SettingGet(ctx, settingAutolock)
	if err != nil || raw != "420000" {
		t.Fatalf("stored autolock = %q err=%v", raw, err)
	}
	if got := loadTestVault(db, &fakeProtector{}).Status().AutoLockMinutes; got != 7 {
		t.Fatalf("reloaded autolock = %d, want 7", got)
	}
	if err := v.SetAutoLock(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := loadTestVault(db, &fakeProtector{}).Status().AutoLockMinutes; got != 0 {
		t.Fatalf("reloaded disabled autolock = %d, want 0", got)
	}
}

func TestLoadIgnoresOutOfRangeAutoLock(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if err := db.SettingSet(ctx, settingAutolock, "999999999"); err != nil {
		t.Fatal(err)
	}
	if got := loadTestVault(db, &fakeProtector{}).Status().AutoLockMinutes; got != 30 {
		t.Fatalf("out-of-range stored autolock = %d, want default 30", got)
	}
	if err := db.SettingSet(ctx, settingAutolock, "not-a-number"); err != nil {
		t.Fatal(err)
	}
	if got := loadTestVault(db, &fakeProtector{}).Status().AutoLockMinutes; got != 30 {
		t.Fatalf("corrupt stored autolock = %d, want default 30", got)
	}
}

func TestAutoLockAppliesLiveToTimer(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := int64(1000)
	v := loadTestVault(db, &fakeProtector{}, func(v *Vault) { v.now = func() int64 { return now } })
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.SetAutoLock(ctx, 2); err != nil {
		t.Fatal(err)
	}
	now += 2 * 60_000
	v.AutoLockIfIdle()
	if !v.Status().Unlocked {
		t.Fatal("idle exactly at the configured threshold must remain unlocked")
	}
	now++
	v.AutoLockIfIdle()
	if v.Status().Unlocked {
		t.Fatal("vault should lock beyond the configured threshold")
	}
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.SetAutoLock(ctx, 10); err != nil {
		t.Fatal(err)
	}
	now += 5 * 60_000
	v.AutoLockIfIdle()
	if !v.Status().Unlocked {
		t.Fatal("raising the threshold must keep the vault unlocked")
	}
	if err := v.SetAutoLock(ctx, 3); err != nil {
		t.Fatal(err)
	}
	v.AutoLockIfIdle()
	if v.Status().Unlocked {
		t.Fatal("lowering the threshold below the current idle must lock on the next tick")
	}
}

func TestAutoLockConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = v.SetAutoLock(ctx, uint64((i*50+j)%1441))
				v.AutoLockIfIdle()
				_ = v.Status()
			}
		}(i)
	}
	wg.Wait()
}
