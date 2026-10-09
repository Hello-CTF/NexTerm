package agent

import (
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// hello 上报的 state digest 必须与设备端 supervisor 按同一规则
// (sha256(identity\x00绝对状态目录)) 推导, 否则代管 attach/create 过不了
// helper 的 hello 校验。
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
	if hello := agentRuntime.hello(""); hello.StateDigest != want {
		t.Fatalf("hello state digest = %q, want %q", hello.StateDigest, want)
	}
	if hello := agentRuntime.hello("bridge-1"); hello.StateDigest != want {
		t.Fatalf("bridge hello state digest = %q, want %q", hello.StateDigest, want)
	}
}
