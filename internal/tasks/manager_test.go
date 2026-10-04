package tasks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	ownerA = Owner{Kind: "session", ID: "a"}
	ownerB = Owner{Kind: "session", ID: "b"}
)

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
}

func shell(script string) Command {
	return Command{Path: "/bin/sh", Args: []string{"-c", script}}
}

func gatedCommand(gate, before, after string) Command {
	return shell(fmt.Sprintf("printf '%%s' %q; while [ ! -f %q ]; do sleep 0.05; done; printf '%%s' %q",
		before, gate, after))
}

func openGate(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testManager(t *testing.T, tweak func(*Options)) *Manager {
	t.Helper()
	opts := Options{Dir: t.TempDir()}
	if tweak != nil {
		tweak(&opts)
	}
	m, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func reopenManager(t *testing.T, dir string) *Manager {
	t.Helper()
	m, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func waitTerminal(t *testing.T, m *Manager, owner Owner, id string) Info {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, err := m.Get(context.Background(), owner, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if info.State.Terminal() {
			return info
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s still %s", id, info.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestRunSyncLeavesNoGarbage(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	res, err := m.Run(ctx, ownerA, shell("printf hello"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Detached || res.Info.Detached || res.Info.State != StateSucceeded {
		t.Fatalf("unexpected sync result: %+v", res)
	}
	if res.Info.ExitCode == nil || *res.Info.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %+v", res.Info.ExitCode)
	}
	if string(res.Output.Head) != "hello" || len(res.Output.Tail) != 0 || res.Output.Total != 5 {
		t.Fatalf("unexpected sync output: %+v", res.Output)
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("sync command left spool garbage: %v", names)
	}
	infos, err := m.List(ctx, ownerA)
	if err != nil || len(infos) != 0 {
		t.Fatalf("sync command left task records: %+v err=%v", infos, err)
	}
	if _, err := m.Get(ctx, ownerA, res.Info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sync task should not stay addressable, got %v", err)
	}

	failed, err := m.Run(ctx, ownerA, shell("printf oops; exit 3"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Info.State != StateFailed || failed.Info.ExitCode == nil || *failed.Info.ExitCode != 3 {
		t.Fatalf("unexpected failure result: %+v", failed.Info)
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("failed sync command left garbage: %v", names)
	}
}

func TestRunStartFailureLeavesNoGarbage(t *testing.T) {
	m := testManager(t, nil)
	if _, err := m.Run(context.Background(), ownerA, Command{Path: "/nonexistent/nexterm-task-test"}, time.Second); err == nil {
		t.Fatal("expected start error")
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("start failure left garbage: %v", names)
	}
}

func TestRunNoDetachMode(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	res, err := m.Run(ctx, ownerA, shell("printf hi"), -1)
	if err != nil || res.Detached || res.Info.State != StateSucceeded {
		t.Fatalf("unexpected no-detach result: %+v err=%v", res, err)
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("no-detach command left garbage: %v", names)
	}

	gate := filepath.Join(t.TempDir(), "gate")
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := m.Run(cancelCtx, ownerA, gatedCommand(gate, "x", "y"), -1); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if names := dirEntries(t, m.dir); len(names) != 0 {
		t.Fatalf("cancelled no-detach command left garbage: %v", names)
	}
}

func TestDetachRetainsHeadAndTail(t *testing.T) {
	requireShell(t)
	m := testManager(t, func(o *Options) {
		o.HeadBytes = 32
		o.TailBytes = 64
	})
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	script := fmt.Sprintf("printf 'AAAAAAAAAAAAAAAA'; while [ ! -f %q ]; do sleep 0.05; done; awk 'BEGIN{for(i=0;i<200;i++)printf \"B\"}'", gate)
	res, err := m.Run(ctx, ownerA, shell(script), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detached || res.Info.State != StateRunning {
		t.Fatalf("expected detached running task: %+v", res)
	}
	var detachOut Output
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := m.Output(ctx, ownerA, res.Info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Total == 16 && string(current.Head) == "AAAAAAAAAAAAAAAA" {
			detachOut = current
			break
		}
		if current.Total > 16 || time.Now().After(deadline) {
			t.Fatalf("pre-detach head not captured: %+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}

	openGate(t, gate)
	info := waitTerminal(t, m, ownerA, res.Info.ID)
	if info.State != StateSucceeded || !info.Detached {
		t.Fatalf("unexpected final state: %+v", info)
	}
	if info.OutputBytes != 216 || info.DroppedBytes != 120 {
		t.Fatalf("unexpected retained counters: %+v", info)
	}
	out, err := m.Output(ctx, ownerA, res.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 216 || out.Dropped != 120 || len(out.Head) != 32 || len(out.Tail) != 64 {
		t.Fatalf("unexpected bounded output: total=%d dropped=%d head=%d tail=%d",
			out.Total, out.Dropped, len(out.Head), len(out.Tail))
	}
	if !bytes.HasPrefix(out.Head, detachOut.Head) {
		t.Fatalf("pre-detach head lost: before=%q after=%q", detachOut.Head, out.Head)
	}
	if string(out.Head[:16]) != "AAAAAAAAAAAAAAAA" || string(out.Head[16:]) != "BBBBBBBBBBBBBBBB" {
		t.Fatalf("unexpected head content: %q", out.Head)
	}
	if string(out.Tail) != strings.Repeat("B", 64) {
		t.Fatalf("unexpected tail content: %q", out.Tail)
	}

	head, err := m.ReadOutput(ctx, ownerA, res.Info.ID, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(head.Data) != "AAAAAAAA" || head.Offset != 0 || head.Next != 8 || head.Total != 216 || head.Gap {
		t.Fatalf("unexpected head chunk: %+v", head)
	}
	gap, err := m.ReadOutput(ctx, ownerA, res.Info.ID, 40, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !gap.Gap || gap.Offset != 152 || gap.Next != 168 || string(gap.Data) != strings.Repeat("B", 16) {
		t.Fatalf("expected gap clamp into tail: %+v", gap)
	}
	tail, err := m.ReadOutput(ctx, ownerA, res.Info.ID, gap.Next, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Gap || tail.Next != 216 || len(tail.Data) != 48 {
		t.Fatalf("unexpected tail continuation: %+v", tail)
	}
	end, err := m.ReadOutput(ctx, ownerA, res.Info.ID, 216, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(end.Data) != 0 || end.Next != 216 {
		t.Fatalf("unexpected end-of-stream chunk: %+v", end)
	}
	if _, err := m.ReadOutput(ctx, ownerA, res.Info.ID, -1, 16); !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("expected invalid offset, got %v", err)
	}
}

func TestPartialOutputWhileRunning(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "start;", "end"), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detached {
		t.Fatalf("expected detach: %+v", res)
	}
	var chunk Chunk
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := m.ReadOutput(ctx, ownerA, res.Info.ID, 0, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if current.Next == 6 && string(current.Data) == "start;" {
			chunk = current
			break
		}
		if current.Next > 6 || time.Now().After(deadline) {
			t.Fatalf("unexpected partial chunk: %+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	openGate(t, gate)
	waitTerminal(t, m, ownerA, res.Info.ID)
	rest, err := m.ReadOutput(ctx, ownerA, res.Info.ID, chunk.Next, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest.Data) != "end" || rest.Total != 9 {
		t.Fatalf("output did not continue after detach: %+v", rest)
	}
}

func TestDetachOnContextCancel(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	gate := filepath.Join(t.TempDir(), "gate")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detached || res.Info.State != StateRunning {
		t.Fatalf("caller cancellation should detach, got %+v", res)
	}
	info, err := m.Kill(context.Background(), ownerA, res.Info.ID)
	if err != nil || info.State != StateKilled {
		t.Fatalf("cleanup kill failed: %+v err=%v", info, err)
	}
}

func TestKillSingleTerminalState(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detached {
		t.Fatalf("expected immediate detach: %+v", res)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := m.Kill(ctx, ownerA, res.Info.ID)
			if err != nil {
				t.Errorf("kill: %v", err)
				return
			}
			if info.State != StateKilled {
				t.Errorf("concurrent kill observed %s", info.State)
			}
		}()
	}
	wg.Wait()
	info := waitTerminal(t, m, ownerA, res.Info.ID)
	if info.State != StateKilled || info.ExitCode != nil {
		t.Fatalf("unexpected terminal state: %+v", info)
	}
	again, err := m.Kill(ctx, ownerA, res.Info.ID)
	if err != nil || again.State != StateKilled {
		t.Fatalf("kill must be idempotent: %+v err=%v", again, err)
	}

	natural, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	openGate(t, gate)
	done := waitTerminal(t, m, ownerA, natural.Info.ID)
	if done.State != StateSucceeded {
		t.Fatalf("unexpected natural completion: %+v", done)
	}
	after, err := m.Kill(ctx, ownerA, natural.Info.ID)
	if err != nil || after.State != StateSucceeded || after.ExitCode == nil || *after.ExitCode != 0 {
		t.Fatalf("kill must not rewrite a terminal state: %+v err=%v", after, err)
	}
}

func TestObjectOwnership(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, ownerB, res.Info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get: %v", err)
	}
	if _, err := m.Output(ctx, ownerB, res.Info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner output: %v", err)
	}
	if _, err := m.ReadOutput(ctx, ownerB, res.Info.ID, 0, 16); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner read: %v", err)
	}
	if _, err := m.Kill(ctx, ownerB, res.Info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner kill: %v", err)
	}
	otherKind := Owner{Kind: "agent", ID: ownerA.ID}
	if _, err := m.Get(ctx, otherKind, res.Info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-kind get: %v", err)
	}
	infos, err := m.List(ctx, ownerB)
	if err != nil || len(infos) != 0 {
		t.Fatalf("cross-owner list: %+v err=%v", infos, err)
	}
	info, err := m.Get(ctx, ownerA, res.Info.ID)
	if err != nil || info.State != StateRunning {
		t.Fatalf("foreign kill attempts must not stop the task: %+v err=%v", info, err)
	}
	if _, err := m.Get(ctx, Owner{}, res.Info.ID); !errors.Is(err, ErrInvalidOwner) {
		t.Fatalf("expected invalid owner, got %v", err)
	}
	killed, err := m.Kill(ctx, ownerA, res.Info.ID)
	if err != nil || killed.State != StateKilled {
		t.Fatalf("owner kill failed: %+v err=%v", killed, err)
	}
}

func TestAuthorizeHook(t *testing.T) {
	requireShell(t)
	denied := errors.New("denied by scope")
	allowKill := false
	var mu sync.Mutex
	var actions []Action
	m := testManager(t, func(o *Options) {
		o.Hooks.Authorize = func(_ context.Context, action Action, owner Owner, info *Info) error {
			mu.Lock()
			actions = append(actions, action)
			mu.Unlock()
			if owner != ownerA {
				return denied
			}
			if action == ActionKill && !allowKill {
				return denied
			}
			if (action == ActionStart || action == ActionList) && info != nil {
				t.Errorf("start/list must not receive a task: %+v", info)
			}
			if action == ActionKill && info == nil {
				t.Error("kill must receive the task snapshot")
			}
			return nil
		}
	})
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(ctx, ownerA); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Kill(ctx, ownerA, res.Info.ID); !errors.Is(err, denied) {
		t.Fatalf("expected scoped denial, got %v", err)
	}
	info, err := m.Get(ctx, ownerA, res.Info.ID)
	if err != nil || info.State != StateRunning {
		t.Fatalf("denied kill must leave the task running: %+v err=%v", info, err)
	}
	if _, err := m.Run(ctx, ownerB, shell("printf x"), 0); !errors.Is(err, denied) {
		t.Fatalf("expected start denial, got %v", err)
	}
	allowKill = true
	if _, err := m.Kill(ctx, ownerA, res.Info.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[Action]bool{}
	for _, action := range actions {
		seen[action] = true
	}
	for _, action := range []Action{ActionStart, ActionList, ActionGet, ActionKill} {
		if !seen[action] {
			t.Errorf("authorize hook never saw %s", action)
		}
	}
}

func TestCloseInterruptsRunning(t *testing.T) {
	requireShell(t)
	m := testManager(t, nil)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	res, err := m.Run(ctx, ownerA, gatedCommand(gate, "before", "after"), 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := m.Output(ctx, ownerA, res.Info.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(out.Head) == "before" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial output missing: %+v", out)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, ownerA, res.Info.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected closed manager, got %v", err)
	}
	reopened := reopenManager(t, m.dir)
	info, err := reopened.Get(ctx, ownerA, res.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateInterrupted || info.EndedAt == nil {
		t.Fatalf("expected interrupted after shutdown: %+v", info)
	}
	out, err := reopened.Output(ctx, ownerA, res.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Head) != "before" {
		t.Fatalf("output lost across shutdown: %+v", out)
	}
}

func TestRetentionBounds(t *testing.T) {
	requireShell(t)
	counter := 0
	m := testManager(t, func(o *Options) {
		o.MaxRetained = 2
		o.Hooks.NewID = func() string {
			counter++
			return fmt.Sprintf("task-%03d", counter)
		}
	})
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	ids := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		res, err := m.Run(ctx, ownerA, gatedCommand(gate, "x", "y"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Detached {
			t.Fatalf("run %d did not detach: %+v", i, res)
		}
		ids = append(ids, res.Info.ID)
	}
	openGate(t, gate)
	deadline := time.Now().Add(15 * time.Second)
	for {
		infos, err := m.List(ctx, ownerA)
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) == 2 && infos[0].ID == ids[2] && infos[1].ID == ids[3] &&
			infos[0].State.Terminal() && infos[1].State.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retention did not converge: %+v", infos)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, evicted := range ids[:2] {
		if _, err := m.Get(ctx, ownerA, evicted); !errors.Is(err, ErrNotFound) {
			t.Fatalf("evicted task %s still readable: %v", evicted, err)
		}
	}
	names := dirEntries(t, m.dir)
	if len(names) != 8 {
		t.Fatalf("expected 4 files per retained task, got %v", names)
	}
	for _, name := range names {
		base, ok := taskFileBase(name)
		if !ok || (base != ids[2] && base != ids[3]) {
			t.Fatalf("unexpected file after eviction: %s", name)
		}
	}
}

func TestOrphanCleanupOnOpen(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"orphan.head":    "h",
		"orphan.tail":    "t",
		"orphan.idx":     "{}",
		"orphan.compact": "{}",
		"bad.json":       "not json",
		"bad.head":       "x",
		"junk.json.tmp":  "{}",
		"unknown.txt":    "keep",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := reopenManager(t, dir)
	names := dirEntries(t, dir)
	if len(names) != 1 || names[0] != "unknown.txt" {
		t.Fatalf("orphan sweep left unexpected files: %v", names)
	}
	infos, err := m.List(context.Background(), ownerA)
	if err != nil || len(infos) != 0 {
		t.Fatalf("unexpected tasks after cleanup: %+v err=%v", infos, err)
	}
}
