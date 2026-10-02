package tasks

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"
)

// TestKillDuringStartup forces a kill to land after the task is registered
// but before the process handle is published. The terminal state must stay
// killed, the process must still be reaped, and a non-detached task must
// leave no files behind.
func TestKillDuringStartup(t *testing.T) {
	requireShell(t)
	starting := make(chan struct{})
	release := make(chan struct{})
	m := testManager(t, func(o *Options) {
		o.Starter = StarterFunc(func(ctx context.Context, cmd Command, output io.Writer) (Process, error) {
			close(starting)
			<-release
			return LocalStarter{}.Start(ctx, cmd, output)
		})
	})
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	type runResult struct {
		res Result
		err error
	}
	finished := make(chan runResult, 1)
	go func() {
		res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
		finished <- runResult{res: res, err: err}
	}()
	<-starting
	infos, err := m.List(ctx, ownerA)
	if err != nil || len(infos) != 1 {
		t.Fatalf("startup task not registered: %+v err=%v", infos, err)
	}
	info, err := m.Kill(ctx, ownerA, infos[0].ID)
	if err != nil || info.State != StateKilled {
		t.Fatalf("startup kill failed: %+v err=%v", info, err)
	}
	close(release)
	select {
	case outcome := <-finished:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.res.Info.State != StateKilled {
			t.Fatalf("startup kill lost its terminal state: %+v", outcome.res.Info)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not finish after startup kill")
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("startup kill left garbage: %v", names)
	}
}
