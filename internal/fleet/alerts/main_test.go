package alerts

import (
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// TestMain keeps account creation at the minimum argon2id cost.
func TestMain(m *testing.M) {
	vault.UseMinimumArgon2ForTesting()
	os.Exit(m.Run())
}
