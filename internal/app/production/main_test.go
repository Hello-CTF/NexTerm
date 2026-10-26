package production

import (
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// TestMain lets local helper tests start this binary as the helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == supervisor.HelperCommand {
		os.Exit(supervisor.RunHelperCLI(os.Args[2:]))
	}
	os.Exit(m.Run())
}
