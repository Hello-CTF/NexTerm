package agent

import (
	"path/filepath"
	"sync"
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

func TestRuntimeHelloCarriesTheStateDirsHelperInstanceID(t *testing.T) {
	dataDir := t.TempDir()
	agentRuntime := newTestRuntime(t, dataDir, []BaseURLEntry{{URL: "http://127.0.0.1:1"}}, &fakeManager{})
	hello := agentRuntime.hello("", "")
	stateDir, err := filepath.Abs(filepath.Join(dataDir, "durable", "supervisor"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := supervisor.HelperInstanceID(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 32 || hello.HelperInstanceID != want {
		t.Fatalf("hello instance id = %q, want %q", hello.HelperInstanceID, want)
	}
	if again := agentRuntime.hello("bridge-1", PurposeTerminal); again.HelperInstanceID != want {
		t.Fatalf("second hello instance id = %q, want it unchanged %q", again.HelperInstanceID, want)
	}
}

func TestRuntimeReconcileFollowsTheInstanceIDToANewDirectory(t *testing.T) {
	first, second := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	dir := first
	var mu sync.Mutex
	agentRuntime := newStateRuntime(t, Options{DataDir: t.TempDir(), StateDirSource: func() string {
		mu.Lock()
		defer mu.Unlock()
		return dir
	}})
	one := agentRuntime.hello("", "").HelperInstanceID
	mu.Lock()
	dir = second
	mu.Unlock()
	two := agentRuntime.hello("", "").HelperInstanceID
	if one == "" || two == "" || one == two {
		t.Fatalf("instance ids %q then %q; want different non-empty ids for different directories", one, two)
	}
}
