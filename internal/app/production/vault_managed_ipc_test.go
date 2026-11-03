package production

import (
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

var vaultManagementCommands = []string{
	"vault_init_master", "vault_init_dpapi", "vault_unlock", "vault_lock", "vault_set_autolock", "vault_change_password",
}

func TestServerVaultOmitsManagementCommands(t *testing.T) {
	ctx := t.Context()
	database := newKnownHostOwnerTestDB(t)
	credentialVault := vault.Load(ctx, database)
	for _, desktop := range []bool{false, true} {
		dispatcher := ipc.NewDispatcher()
		if err := registerVaultCommands(dispatcher, credentialVault, database, nil, desktop); err != nil {
			t.Fatal(err)
		}
		registered := map[string]bool{}
		for _, command := range dispatcher.Commands() {
			registered[command] = true
		}
		for _, command := range vaultManagementCommands {
			if registered[command] != desktop {
				t.Fatalf("desktop=%v %s registered=%v", desktop, command, registered[command])
			}
		}
		if !registered["vault_status"] || !registered["vault_set_credential"] {
			t.Fatalf("desktop=%v: vault_status or credential commands missing", desktop)
		}
		var status struct {
			Managed bool `json:"managed"`
		}
		response := dispatcher.Dispatch(ctx, ipc.Request{Command: "vault_status", Args: json.RawMessage(`{}`)}, ipc.Environment{})
		requireStoreTestResponse(t, response, &status)
		if status.Managed == desktop {
			t.Fatalf("desktop=%v managed=%v", desktop, status.Managed)
		}
	}
}
