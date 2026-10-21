package session

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

// helperProcessEnv marks a subprocess spawned as a test helper rather than
// as a test runner: the helper runs only its helper entrypoint, never the
// suite, so the permanent goroutine-leak gate must not run in it.
const helperProcessEnv = "NEXTERM_SESSION_TEST_HELPER"

// TestMain is the package's permanent goroutine-leak gate: every test
// process must finish with no leaked goroutines — pumps, coalescers,
// fanouts, response writers, reconnect and idle loops all exit when their
// manager closes. The gate runs unconditionally on the test path; only a
// helper-process invocation branches away from it.
func TestMain(m *testing.M) {
	if os.Getenv(helperProcessEnv) != "" {
		os.Exit(m.Run())
	}
	goleak.VerifyTestMain(m)
}
