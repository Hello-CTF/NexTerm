package session

import (
	"bytes"
	"os"
	"os/exec"
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

const goleakGateProbeEnv = "NEXTERM_GOLEAK_GATE_PROBE"

func TestGoleakGateDetectsIntentionalLeak(t *testing.T) {
	if os.Getenv(goleakGateProbeEnv) == "1" {
		go goleakGateIntentionalLeak()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGoleakGateDetectsIntentionalLeak$", "-test.count=1")
	cmd.Env = append(os.Environ(), goleakGateProbeEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("leak gate accepted an intentional goroutine leak:\n%s", output)
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("run intentional-leak probe: %v", err)
	}
	if !bytes.Contains(output, []byte("found unexpected goroutines")) || !bytes.Contains(output, []byte("goleakGateIntentionalLeak")) {
		t.Fatalf("leak probe failed without goleak identifying the intentional leak:\n%s", output)
	}
}

func goleakGateIntentionalLeak() {
	select {}
}
