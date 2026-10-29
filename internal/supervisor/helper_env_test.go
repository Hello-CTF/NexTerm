//go:build unix

package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestStaleReasonByBuildAndStartEnvironment(t *testing.T) {
	local := RuntimeMetadata{Version: "1.3.1", SHA256: strings.Repeat("a", 64)}
	other := &RuntimeMetadata{Version: "1.3.0", SHA256: strings.Repeat("b", 64)}
	expected := StartEnv{Shell: "/bin/zsh", PathHash: HashPath("/usr/bin:/bin")}
	for name, tc := range map[string]struct {
		helper RunningHelper
		want   string
	}{
		"same build and environment":   {RunningHelper{Protocol: ProtocolVersion, Runtime: &local, StartEnv: &expected}, ""},
		"same build, other shell":      {RunningHelper{Protocol: ProtocolVersion, Runtime: &local, StartEnv: &StartEnv{Shell: "/bin/bash", PathHash: expected.PathHash}}, "env"},
		"same build, other PATH":       {RunningHelper{Protocol: ProtocolVersion, Runtime: &local, StartEnv: &StartEnv{Shell: expected.Shell, PathHash: HashPath("/go/bin")}}, "env"},
		"older build wins":             {RunningHelper{Protocol: ProtocolVersion, Runtime: other, StartEnv: &StartEnv{Shell: "/bin/bash"}}, "build"},
		"helper without the fields":    {RunningHelper{Protocol: ProtocolVersion, Runtime: &local}, ""},
		"older build without fields":   {RunningHelper{Protocol: ProtocolVersion, Runtime: other}, "build"},
		"other protocol is not judged": {RunningHelper{Protocol: ProtocolVersion - 1, Runtime: &local, StartEnv: &StartEnv{Shell: "/bin/bash"}}, ""},
	} {
		if got := tc.helper.StaleReason(local, expected); got != tc.want {
			t.Errorf("%s: StaleReason = %q, want %q", name, got, tc.want)
		}
	}
}

func TestStaleEnvHasNoNotice(t *testing.T) {
	build := HelperBuild{Stale: true, StaleReason: "env", StartShell: "/bin/bash", ExpectedShell: "/bin/zsh", Sessions: 1, SessionsKnown: true}
	if got := build.Notice(); got != "" {
		t.Fatalf("a helper stale only by environment is replaced silently, got notice %q", got)
	}
}

func TestEnsureHelperBuildReplacesEnvStaleHelperOnlyWhenIdle(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	config := HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second}
	helper, err := ConnectHelper(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	local, err := CurrentRuntimeMetadata()
	if err != nil {
		t.Fatal(err)
	}
	defer func(previous func() StartEnv) { expectedStartEnv = previous }(expectedStartEnv)

	// The helper started in this environment: healthy, nothing happens.
	build, err := ensureHelperBuild(ctx, config, local)
	if err != nil || build.Stale || build.Replaced {
		t.Fatalf("healthy helper: %+v, %v", build, err)
	}

	// A session keeps an environment-stale helper alone.
	expectedStartEnv = func() StartEnv { return StartEnv{Shell: "/bin/zsh", PathHash: HashPath("/other")} }
	attachment, err := NewRemoteProvider(helper.Client()).Create(ctx, base.DurableCreateOptions{
		ID: ids.New(), Command: []string{"/bin/sh", "-c", "sleep 300"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = attachment.Close()
	pid := helper.PID()
	build, err = ensureHelperBuild(ctx, config, local)
	if err != nil || !build.Stale || build.StaleReason != "env" || build.Replaced || build.Sessions != 1 {
		t.Fatalf("busy helper: %+v, %v; want stale (env), deferred", build, err)
	}
	if running, err := InspectHelper(ctx, stateDir); err != nil || running.PID != pid {
		t.Fatalf("a helper with sessions must stay: %+v, %v", running, err)
	}
	data, _ := os.ReadFile(filepath.Join(stateDir, "helper.log"))
	logged := string(data)
	for _, event := range []string{"stale_detected", "stale_deferred"} {
		if !strings.Contains(logged, event) {
			t.Errorf("helper.log lacks %s:\n%s", event, logged)
		}
	}
}

func TestEnsureHelperBuildReplacesIdleEnvStaleHelper(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	config := HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second}
	helper, err := ConnectHelper(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	local, err := CurrentRuntimeMetadata()
	if err != nil {
		t.Fatal(err)
	}
	defer func(previous func() StartEnv) { expectedStartEnv = previous }(expectedStartEnv)
	expectedStartEnv = func() StartEnv { return StartEnv{Shell: "/bin/zsh", PathHash: HashPath("/other")} }
	oldPID := helper.PID()
	build, err := ensureHelperBuild(ctx, config, local)
	if err != nil || !build.Replaced || build.StaleReason != "env" {
		t.Fatalf("idle helper: %+v, %v; want replaced (env)", build, err)
	}
	running, err := InspectHelper(ctx, stateDir)
	if err != nil || running.PID == oldPID {
		t.Fatalf("a new helper must serve: %+v, %v (old pid %d)", running, err, oldPID)
	}
	data, _ := os.ReadFile(filepath.Join(stateDir, "helper.log"))
	if !strings.Contains(string(data), "stale_replaced") {
		t.Errorf("helper.log lacks stale_replaced:\n%s", data)
	}
}

func TestHelperReportsItsStartEnvironment(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	if _, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second}); err != nil {
		t.Fatal(err)
	}
	running, err := InspectHelper(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if running.StartEnv == nil || *running.StartEnv != CurrentStartEnv() {
		t.Fatalf("start env = %+v, want %+v", running.StartEnv, CurrentStartEnv())
	}
}
