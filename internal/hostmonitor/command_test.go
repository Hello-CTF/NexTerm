package hostmonitor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// runLocally runs a sample command the way an SSH exec channel would hand it to the login shell.
func runLocally(t *testing.T, command string, env []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run sample command: %v\n%s", err, out)
	}
	return string(out)
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the sample script reads /proc of a Linux host")
	}
}

func TestSampleCommandOnThisHost(t *testing.T) {
	requireLinux(t)
	raw, err := ParseOutput(runLocally(t, SampleCommand(true), os.Environ()))
	if err != nil {
		t.Fatal(err)
	}
	if raw.OS != "Linux" || raw.CPU == nil || raw.Memory == nil || raw.Load == nil || raw.Uptime == nil || len(raw.Disks) == 0 || raw.DisksTimedOut {
		t.Fatalf("raw = %+v", raw)
	}
}

func TestSampleCommandWithoutDisksSkipsDF(t *testing.T) {
	requireLinux(t)
	if strings.Contains(SampleCommand(false), "df -P") {
		t.Fatal("the command without disks still runs df")
	}
	raw, err := ParseOutput(runLocally(t, SampleCommand(false), os.Environ()))
	if err != nil || raw.Memory == nil || len(raw.Disks) != 0 {
		t.Fatalf("raw = %+v, %v", raw, err)
	}
}

func TestSampleCommandStopsHungDF(t *testing.T) {
	requireLinux(t)
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not installed")
	}
	stubs := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "df"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := dfTimeoutSeconds
	dfTimeoutSeconds = "1"
	t.Cleanup(func() { dfTimeoutSeconds = previous })

	env := append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now()
	raw, err := ParseOutput(runLocally(t, SampleCommand(true), env))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the hung df was not stopped: %v", elapsed)
	}
	if !raw.DisksTimedOut || raw.Memory == nil {
		t.Fatalf("raw = %+v", raw)
	}
}
