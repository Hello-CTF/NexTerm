package fleetserver

import (
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// TestMain keeps password hashing and key wrapping at the minimum argon2id
// cost: the device and share tests create accounts throughout, and the
// production cost is covered by the account, vault and sync packages. The agents of the direct-connection tests
// reach httptest servers over plain HTTP, which production refuses for direct connections.
func TestMain(m *testing.M) {
	vault.UseMinimumArgon2ForTesting()
	agent.AllowPlainHTTPP2PForTesting()
	os.Exit(m.Run())
}
