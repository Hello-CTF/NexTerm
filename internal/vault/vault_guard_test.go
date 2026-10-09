package vault

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestInitMasterRefusesWhenSettingsContainSecretEnvelope(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	envelope := `{"version":1,"profiles":[{"id":"p1","name":"n","apiKey":"enc:v1:dGFtcGVyZWQ"}],"activeId":"p1"}`
	if err := db.SettingSet(ctx, store.AIProfilesSettingKey, envelope); err != nil {
		t.Fatal(err)
	}
	v := loadTestVault(db, &fakeProtector{})
	requireVaultCode(t, v.InitMaster(ctx, "correct-password"), ipc.CodeCrypto)
	if v.Status().Initialized {
		t.Fatal("vault must stay uninitialized while settings hold enc:v1: ciphertext")
	}
	if err := db.SettingDelete(ctx, store.AIProfilesSettingKey); err != nil {
		t.Fatal(err)
	}
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatalf("init after cleanup: %v", err)
	}
}

func TestInitDPAPIRefusesWhenSettingsContainSecretEnvelope(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if err := db.SettingSet(ctx, "some.setting", "enc:v1:ZGFua29nYWk="); err != nil {
		t.Fatal(err)
	}
	v := loadTestVault(db, &fakeProtector{})
	requireVaultCode(t, v.InitDPAPI(ctx, ""), ipc.CodeCrypto)
	if v.Status().Initialized {
		t.Fatal("vault must stay uninitialized while settings hold enc:v1: ciphertext")
	}
	if err := db.SettingDelete(ctx, "some.setting"); err != nil {
		t.Fatal(err)
	}
	if err := v.InitDPAPI(ctx, ""); err != nil {
		t.Fatalf("init after cleanup: %v", err)
	}
}

func TestInitRefusedWhenSettingsUnreadable(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	v := Load(ctx, db, WithProtector(&fakeProtector{}), func(v *Vault) { v.derive = fastDerive })
	requireVaultCode(t, v.InitMaster(ctx, "correct-password"), ipc.CodeCrypto)
	requireVaultCode(t, v.InitDPAPI(ctx, ""), ipc.CodeCrypto)
	if v.Status().Initialized {
		t.Fatal("vault must not initialize while settings state is unknown")
	}
}
