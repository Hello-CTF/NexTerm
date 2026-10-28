package fleetserver

import (
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// TestMain keeps password hashing and key wrapping at the minimum argon2id
// cost: the device and share tests create accounts throughout, and the
// production cost is covered by the account, vault and sync packages.
func TestMain(m *testing.M) {
	vault.UseMinimumArgon2ForTesting()
	os.Exit(m.Run())
}
