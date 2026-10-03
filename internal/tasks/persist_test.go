package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errMetaFault = errors.New("injected meta write failure")

// faultMetaWrites fails durable record writes, optionally only for matching
// records. The returned restore function re-enables writes mid-test.
func faultMetaWrites(t *testing.T, match func(info Info) bool) (restore func()) {
	t.Helper()
	restore = metaFileWriter.swap(func(dir string, info Info) error {
		if match == nil || match(info) {
			return errMetaFault
		}
		return writeMetaFile(dir, info)
	})
	t.Cleanup(restore)
	return restore
}

func readMeta(t *testing.T, dir, id string) Info {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestKillPersistenceFault(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	restore := faultMetaWrites(t, nil)
	info, err := m.Kill(ctx, ownerA, res.Info.ID)
	if !errors.Is(err, errMetaFault) {
		t.Fatalf("kill must surface the persistence failure, got %v", err)
	}
	if info.State != StateKilled || info.PersistError == "" {
		t.Fatalf("kill outcome not exposed: %+v", info)
	}
	if onDisk := readMeta(t, m.dir, res.Info.ID); onDisk.State != StateRunning {
		t.Fatalf("fault injection should leave a stale running record: %+v", onDisk)
	}
	restore()
	retried, err := m.Kill(ctx, ownerA, res.Info.ID)
	if err != nil {
		t.Fatalf("kill retry must persist the terminal state: %v", err)
	}
	if retried.State != StateKilled || retried.PersistError != "" {
		t.Fatalf("retry did not clear the failure: %+v", retried)
	}
	reopened := reopenManager(t, m.dir)
	durable, err := reopened.Get(ctx, ownerA, res.Info.ID)
	if err != nil || durable.State != StateKilled {
		t.Fatalf("terminal state not durable after retry: %+v err=%v", durable, err)
	}
}

func TestNaturalExitPersistenceFaultAndCloseRetry(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	restore := faultMetaWrites(t, nil)
	openGate(t, gate)
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, err := m.Get(ctx, ownerA, res.Info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if info.State == StateSucceeded && info.PersistError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persistence failure not recorded on the task: %+v", info)
		}
		time.Sleep(5 * time.Millisecond)
	}
	restore()
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close must retry and succeed: %v", err)
	}
	reopened := reopenManager(t, m.dir)
	durable, err := reopened.Get(ctx, ownerA, res.Info.ID)
	if err != nil || durable.State != StateSucceeded {
		t.Fatalf("Close retry did not persist the terminal state: %+v err=%v", durable, err)
	}
}

func TestCloseSurfacesPersistenceFault(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	if _, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0); err != nil {
		t.Fatal(err)
	}
	restore := faultMetaWrites(t, nil)
	if err := m.Close(ctx); !errors.Is(err, errMetaFault) {
		t.Fatalf("Close must surface persistence failures, got %v", err)
	}
	restore()
}

// TestFaultMetaWritesConcurrentSwap is a regression for the release CI data
// race: faultMetaWrites installed and restored the package-level
// metaFileWriter without synchronizing against the reads in
// writeMetaLocked. It swaps the hook while concurrent meta writes are in
// flight; with the unsynchronized global this trips -race.
func TestFaultMetaWritesConcurrentSwap(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	tk := m.tasks[res.Info.ID]
	m.mu.RUnlock()
	if tk == nil {
		t.Fatal("task missing from manager map")
	}
	const iterations = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			tk.mu.Lock()
			_ = m.writeMetaLocked(tk)
			tk.mu.Unlock()
		}
	}()
	for i := 0; i < iterations; i++ {
		restore := metaFileWriter.swap(func(dir string, info Info) error { return errMetaFault })
		restore()
	}
	<-done
}

func TestRetentionSkipsUnpersisted(t *testing.T) {
	requireShell(t)
	counter := 0
	m := testManager(t, func(o *Options) {
		o.MaxRetained = 1
		o.Hooks.NewID = func() string {
			counter++
			return fmt.Sprintf("task-%03d", counter)
		}
	})
	ctx := context.Background()
	gateA := filepath.Join(t.TempDir(), "gate-a")
	resA, err := m.Run(ctx, ownerA, gatedCommand(gateA, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	restore := faultMetaWrites(t, func(info Info) bool { return info.ID == resA.Info.ID })
	openGate(t, gateA)
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, err := m.Get(ctx, ownerA, resA.Info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if info.State.Terminal() && info.PersistError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fault never recorded: %+v", info)
		}
		time.Sleep(5 * time.Millisecond)
	}
	gateB := filepath.Join(t.TempDir(), "gate-b")
	resB, err := m.Run(ctx, ownerA, gatedCommand(gateB, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	openGate(t, gateB)
	waitTerminal(t, m, ownerA, resB.Info.ID)
	// A is terminal but unpersisted: retention must not evict it, and the
	// newer eligible task alone does not exceed MaxRetained.
	if _, err := m.Get(ctx, ownerA, resA.Info.ID); err != nil {
		t.Fatalf("unpersisted task was evicted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, resA.Info.ID+".head")); err != nil {
		t.Fatalf("unpersisted spool was removed: %v", err)
	}
	restore()
	if _, err := m.Kill(ctx, ownerA, resA.Info.ID); err != nil {
		t.Fatalf("retry after restore failed: %v", err)
	}
	gateC := filepath.Join(t.TempDir(), "gate-c")
	resC, err := m.Run(ctx, ownerA, gatedCommand(gateC, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	openGate(t, gateC)
	waitTerminal(t, m, ownerA, resC.Info.ID)
	deadline = time.Now().Add(15 * time.Second)
	for {
		infos, err := m.List(ctx, ownerA)
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) == 1 && infos[0].ID == resC.Info.ID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retention did not converge after retry: %+v", infos)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
