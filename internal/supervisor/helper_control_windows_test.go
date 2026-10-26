//go:build windows

package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestInspectAndStopHelperOverTheNamedPipe(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcessesWindows(t, stateDir) })
	ctx := context.Background()
	helper, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := NewRemoteProvider(helper.Client()).Create(ctx, base.DurableCreateOptions{
		ID:      ids.New(),
		Command: []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NoExit", "-Command", "Write-Host win-inspect-marker"},
		Env:     []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readPipeUntil(t, attachment, "win-inspect-marker")
	_ = attachment.Close()
	running, err := InspectHelper(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !running.Current() || running.PID != helper.PID() || running.Activity.PTY != 1 {
		t.Fatalf("inspected helper = %+v, want pid %d with one PTY", running, helper.PID())
	}
	stale := running
	stale.Activity.PTY = 0
	if err := StopHelper(ctx, stateDir, stale); !errors.Is(err, ErrHelperChanged) {
		t.Fatalf("stop with consent for fewer sessions = %v, want ErrHelperChanged", err)
	}
	if err := StopHelper(ctx, stateDir, running); err != nil {
		t.Fatal(err)
	}
	select {
	case <-helper.Exited():
	case <-time.After(15 * time.Second):
		t.Fatal("stopped helper did not exit")
	}
	if _, err := InspectHelper(ctx, stateDir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("inspect after stop = %v, want ErrUnavailable", err)
	}
}
