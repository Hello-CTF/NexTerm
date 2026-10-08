package production

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestVaultSetAutoLockCommand(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	dispatcher := ipc.NewDispatcher()
	if err := registerVaultCommands(dispatcher, credentialVault, database, nil); err != nil {
		t.Fatal(err)
	}

	response := dispatchStoreTest(dispatcher, "vault_status", `null`)
	var status vault.Status
	requireStoreTestResponse(t, response, &status)
	if status.AutoLockMinutes != 30 {
		t.Fatalf("default autolock = %d, want 30", status.AutoLockMinutes)
	}

	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_set_autolock", `{"minutes":5}`))
	response = dispatchStoreTest(dispatcher, "vault_status", `null`)
	requireStoreTestResponse(t, response, &status)
	if status.AutoLockMinutes != 5 {
		t.Fatalf("autolock = %d, want 5", status.AutoLockMinutes)
	}

	response = dispatchStoreTest(dispatcher, "vault_set_autolock", `{"minutes":1441}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("out-of-range autolock = %+v, want bad_param", response)
	}
	response = dispatchStoreTest(dispatcher, "vault_set_autolock", `{"minutes":-1}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("negative autolock = %+v, want bad_param", response)
	}
	for name, args := range map[string]string{
		"null minutes": `{"minutes":null}`,
		"empty object": `{}`,
		"null args":    `null`,
	} {
		response = dispatchStoreTest(dispatcher, "vault_set_autolock", args)
		if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
			t.Fatalf("%s = %+v, want bad_param", name, response)
		}
		response = dispatchStoreTest(dispatcher, "vault_status", `null`)
		requireStoreTestResponse(t, response, &status)
		if status.AutoLockMinutes != 5 {
			t.Fatalf("%s changed the setting to %d, want 5", name, status.AutoLockMinutes)
		}
	}

	requireProductionNull(t, dispatchStoreTest(dispatcher, "vault_set_autolock", `{"minutes":0}`))
	if got := vault.Load(ctx, database).Status().AutoLockMinutes; got != 0 {
		t.Fatalf("reloaded autolock = %d, want 0", got)
	}
}
