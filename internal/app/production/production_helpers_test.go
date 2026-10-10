package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func requireProductionNull(t *testing.T, response ipc.Response) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	if string(response.Data) != "null" {
		t.Fatalf("dispatch data = %s, want null", response.Data)
	}
}

func initTestVault(t *testing.T, ctx context.Context, database *store.Store) {
	t.Helper()
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "test-password"); err != nil {
		if err := credentialVault.UnlockMaster(ctx, "test-password"); err != nil {
			t.Fatal(err)
		}
	}
}
