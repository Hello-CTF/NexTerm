package agent

import (
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// The state digest reported in hello must be derived by the same rule as the device-side supervisor
// (sha256(identity\x00absolute state directory)); otherwise managed attach/create cannot pass
// the helper's hello verification.
func TestRuntimeComputeStateDigest(t *testing.T) {
	dataDir := t.TempDir()
	agentRuntime := newTestRuntime(t, dataDir, []BaseURLEntry{{URL: "http://127.0.0.1:1"}}, &fakeManager{})
	agentRuntime.computeStateDigest()

	identity, err := currentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := filepath.Abs(filepath.Join(dataDir, "durable", "supervisor"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := supervisor.StateDigest(stateDir, identity)
	if err != nil {
		t.Fatal(err)
	}
	if agentRuntime.stateDigest != want {
		t.Fatalf("stateDigest = %q, want %q", agentRuntime.stateDigest, want)
	}
	if hello := agentRuntime.hello("", ""); hello.StateDigest != want {
		t.Fatalf("hello state digest = %q, want %q", hello.StateDigest, want)
	}
	if hello := agentRuntime.hello("bridge-1", PurposeTerminal); hello.StateDigest != want {
		t.Fatalf("bridge hello state digest = %q, want %q", hello.StateDigest, want)
	}
}
