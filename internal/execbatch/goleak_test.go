package execbatch

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies the package does not leak goroutines after its tests
// finish. The intentional-leak probe re-executes only itself and must also
// pass through this gate.
func TestMain(m *testing.M) {
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
