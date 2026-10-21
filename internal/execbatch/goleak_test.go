package execbatch

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies the package does not leak goroutines after its tests
// finish. This package has no helper-process branch (it never re-executes
// the test binary), so every run goes through m.Run with no exceptions.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
