package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestartMarksRunningInterrupted(t *testing.T) {
	requireShell(t)
	dir := t.TempDir()
	work := t.TempDir()
	gate := filepath.Join(work, "gate")
	marker := filepath.Join(work, "marker")
	m1, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	script := fmt.Sprintf("echo run >> %q; printf 'boot;'; while [ ! -f %q ]; do sleep 0.05; done", marker, gate)
	res, err := m1.Run(ctx, ownerA, shell(script), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detached {
		t.Fatalf("expected detach: %+v", res)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if raw, err := os.ReadFile(marker); err == nil && strings.Count(string(raw), "run") == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}

	deadline = time.Now().Add(10 * time.Second)
	for {
		out, err := m1.Output(ctx, ownerA, res.Info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(out.Head) == "boot;" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial output missing: %+v", out)
		}
		time.Sleep(5 * time.Millisecond)
	}

	m2 := reopenManager(t, dir)
	info, err := m2.Get(ctx, ownerA, res.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateInterrupted || info.EndedAt == nil || info.ExitCode != nil {
		t.Fatalf("restart must reconcile to interrupted: %+v", info)
	}
	out, err := m2.Output(ctx, ownerA, res.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Head) != "boot;" {
		t.Fatalf("restart lost retained output: %+v", out)
	}
	chunk, err := m2.ReadOutput(ctx, ownerA, res.Info.ID, 0, 1024)
	if err != nil || string(chunk.Data) != "boot;" {
		t.Fatalf("restart incremental read failed: %+v err=%v", chunk, err)
	}
	time.Sleep(200 * time.Millisecond)
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "run") != 1 {
		t.Fatalf("task was re-executed after restart: %q", raw)
	}

	if err := m1.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRestartKeepsTerminalStates(t *testing.T) {
	requireShell(t)
	dir := t.TempDir()
	m1, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	done, err := m1.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	openGate(t, gate)
	waitTerminal(t, m1, ownerA, done.Info.ID)
	killed, err := m1.Run(ctx, ownerA, gatedCommand(filepath.Join(t.TempDir(), "never"), "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m1.Kill(ctx, ownerA, killed.Info.ID); err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m1, ownerA, killed.Info.ID)

	m2 := reopenManager(t, dir)
	succeeded, err := m2.Get(ctx, ownerA, done.Info.ID)
	if err != nil || succeeded.State != StateSucceeded || succeeded.ExitCode == nil || *succeeded.ExitCode != 0 {
		t.Fatalf("terminal record changed across restart: %+v err=%v", succeeded, err)
	}
	stillKilled, err := m2.Get(ctx, ownerA, killed.Info.ID)
	if err != nil || stillKilled.State != StateKilled {
		t.Fatalf("killed record changed across restart: %+v err=%v", stillKilled, err)
	}
	if err := m1.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
