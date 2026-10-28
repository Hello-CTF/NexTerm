package production

import (
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// TestMain lets local helper tests start this binary as the helper, and
// keeps account, vault and bundle key derivation at the minimum argon2id
// cost: most tests here create users or unlock vaults, and the production
// cost is covered by the account, vault and sync packages.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == supervisor.HelperCommand {
		os.Exit(supervisor.RunHelperCLI(os.Args[2:]))
	}
	vault.UseMinimumArgon2ForTesting()
	os.Exit(m.Run())
}
