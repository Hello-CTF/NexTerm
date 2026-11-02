package runbook

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a prompt or run leaves a goroutine behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
