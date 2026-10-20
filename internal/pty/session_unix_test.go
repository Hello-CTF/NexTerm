//go:build unix

package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestSessionOutputResizeAndExitStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := Start(ctx, Config{
		Path: "/bin/sh",
		Args: []string{"-lc", "printf '中文-🚀\\n'; sleep 0.2; stty size; exit 7"},
		Env:  testEnvironment(),
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Resize(context.Background(), 100, 30); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "中文-🚀") || !strings.Contains(string(output), "30 100") {
		t.Fatalf("PTY output = %q", output)
	}
	err = session.Wait(context.Background())
	var exitErr *base.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestSessionRawWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := Start(ctx, Config{Path: "/bin/cat", Env: testEnvironment(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Write([]byte("héllo-🚀\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte{4}); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "héllo-🚀") {
		t.Fatalf("PTY output = %q", output)
	}
	if err := session.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCloseKillsProcessTreeAndIsIdempotent(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "pty-child.pid")
	command := fmt.Sprintf("sleep 1000 & child=$!; printf '%%s' \"$child\" > %s; wait", testShellQuote(pidPath))
	session, err := Start(context.Background(), Config{Path: "/bin/sh", Args: []string{"-lc", command}, Env: testEnvironment(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	pid := readSessionPID(t, pidPath)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	assertSessionProcessGone(t, pid)
}

func TestUnixProcessSnapshotListsCurrentProcessWithState(t *testing.T) {
	output, err := defaultUnixProcessAPI().snapshot()
	if err != nil {
		t.Fatalf("process snapshot = %v", err)
	}
	pid := strconv.Itoa(os.Getpid())
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != pid {
			continue
		}
		if len(fields) != 3 {
			t.Fatalf("current process snapshot = %q, want PID, PGID and STAT", line)
		}
		group, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("current process PGID = %q: %v", fields[1], err)
		}
		expectedGroup, err := syscall.Getpgid(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		if group != expectedGroup {
			t.Fatalf("current process PGID = %d, want %d", group, expectedGroup)
		}
		return
	}
	t.Fatalf("process snapshot does not contain PID %d: %q", os.Getpid(), output)
}

func TestSessionCloseKillsJobControlProcessGroups(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "/bin/zsh"} {
		t.Run(strings.TrimPrefix(shell, "/bin/"), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("shell unavailable: %v", err)
			}
			dir := t.TempDir()
			shellPIDPath := filepath.Join(dir, "shell.pid")
			childPIDPath := filepath.Join(dir, "child.pid")
			var args []string
			if shell == "/bin/zsh" {
				args = []string{"-f", "-i", "+Z"}
			}
			session, err := Start(context.Background(), Config{Path: shell, Args: args, Env: testEnvironment(), Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if _, err := session.Write([]byte(fmt.Sprintf("printf '%%s' \"$$\" > %s\r", testShellQuote(shellPIDPath)))); err != nil {
				t.Fatal(err)
			}
			shellPID := readSessionPID(t, shellPIDPath)
			if _, err := session.Write([]byte(fmt.Sprintf("(sleep 1000) & printf '%%s' $! > %s\r", testShellQuote(childPIDPath)))); err != nil {
				t.Fatal(err)
			}
			childPID := readSessionPID(t, childPIDPath)
			shellGroup, err := syscall.Getpgid(shellPID)
			if err != nil {
				t.Fatal(err)
			}
			childGroup, err := syscall.Getpgid(childPID)
			if err != nil {
				t.Fatal(err)
			}
			if childGroup == shellGroup {
				t.Fatalf("default job control did not separate groups: shell=%d child=%d", shellGroup, childGroup)
			}
			t.Cleanup(func() { _ = syscall.Kill(-childGroup, syscall.SIGKILL) })

			other, err := Start(context.Background(), Config{Path: "/bin/sleep", Args: []string{"1000"}, Env: testEnvironment(), Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Close() })
			unrelated := osexec.Command("sleep", "1000")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = unrelated.Process.Kill()
				_, _ = unrelated.Process.Wait()
			})

			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			assertSessionProcessGone(t, shellPID)
			assertSessionProcessGone(t, childPID)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := other.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unrelated PTY session was killed: %v", err)
			}
			if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated process was killed: %v", err)
			}
		})
	}
}

type fakeUnixSession struct {
	sessionID   int
	processes   map[int]int
	states      map[int]string
	identity    string
	identityErr error
	kills       []int
	onKill      func(*fakeUnixSession, int)
}

func (f *fakeUnixSession) snapshot() ([]byte, error) {
	pids := make([]int, 0, len(f.processes))
	for pid := range f.processes {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	var output strings.Builder
	for _, pid := range pids {
		if f.states[pid] == "" {
			fmt.Fprintf(&output, "%d %d\n", pid, f.processes[pid])
		} else {
			fmt.Fprintf(&output, "%d %d %s\n", pid, f.processes[pid], f.states[pid])
		}
	}
	return []byte(output.String()), nil
}

func (f *fakeUnixSession) getSessionID(pid int) (int, error) {
	if _, ok := f.processes[pid]; !ok {
		return 0, syscall.ESRCH
	}
	return f.sessionID, nil
}

func (f *fakeUnixSession) getProcessGroupID(pid int) (int, error) {
	group, ok := f.processes[pid]
	if !ok {
		return 0, syscall.ESRCH
	}
	return group, nil
}

func (f *fakeUnixSession) getProcessIdentity(pid int) (string, error) {
	if f.identityErr != nil {
		return "", f.identityErr
	}
	if _, ok := f.processes[pid]; !ok {
		return "", syscall.ESRCH
	}
	return f.identity, nil
}

func (f *fakeUnixSession) api() unixProcessAPI {
	return unixProcessAPI{
		snapshot:            f.snapshot,
		getSessionID:        f.getSessionID,
		getProcessGroupID:   f.getProcessGroupID,
		readProcessIdentity: f.getProcessIdentity,
		kill:                f.kill,
	}
}

func (f *fakeUnixSession) tree() *unixProcessTree {
	return f.treeWithAPI(f.api())
}

func (f *fakeUnixSession) treeWithAPI(api unixProcessAPI) *unixProcessTree {
	if f.identity == "" {
		f.identity = "test-leader-identity"
	}
	return &unixProcessTree{process: &os.Process{Pid: f.sessionID}, session: true, identity: f.identity, api: api}
}

func (f *fakeUnixSession) kill(group int, _ syscall.Signal) error {
	if group < 0 {
		group = -group
	}
	f.kills = append(f.kills, group)
	if f.onKill != nil {
		f.onKill(f, group)
	}
	for pid, processGroup := range f.processes {
		if processGroup == group && f.states[pid] != "Z" {
			delete(f.processes, pid)
		}
	}
	return nil
}

func TestUnixSessionKillConvergesWhenJobsArriveDuringCleanup(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100, 200: 200}}
	fake.onKill = func(fake *fakeUnixSession, group int) {
		if group == 200 {
			fake.processes[201] = 201
		}
	}
	tree := fake.tree()
	if err := tree.Kill(); err != nil {
		t.Fatal(err)
	}
	if len(fake.processes) != 0 {
		t.Fatalf("processes left after cleanup: %v; kills = %v", fake.processes, fake.kills)
	}
}

func TestUnixSessionKillConvergesWithZombieMembers(t *testing.T) {
	fake := &fakeUnixSession{
		sessionID: 100,
		processes: map[int]int{100: 100, 200: 200, 201: 201},
		states:    map[int]string{200: "Z"},
	}
	tree := fake.tree()
	if err := tree.Kill(); err != nil {
		t.Fatalf("cleanup with zombie members = %v; kills = %v", err, fake.kills)
	}
	if len(fake.kills) != 2 || fake.kills[0] != 201 || fake.kills[1] != 100 {
		t.Fatalf("kills with zombie members = %v, want live job then leader", fake.kills)
	}
}

func TestUnixSessionKillReportsNonConvergence(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100, 200: 200}}
	fake.onKill = func(fake *fakeUnixSession, _ int) {
		next := 1000 + len(fake.kills)
		fake.processes[next] = next
	}
	tree := fake.tree()
	if err := tree.Kill(); err == nil {
		t.Fatal("continuously growing session cleanup reported success")
	}
	if len(fake.kills) != 32 {
		t.Fatalf("kill rounds before non-convergence error = %d, want 32", len(fake.kills))
	}
}

func TestUnixSessionKillRevalidatesMemberBeforeGroupKill(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100, 101: 101, 102: 102}}
	fake.onKill = func(fake *fakeUnixSession, group int) {
		if group == 101 {
			delete(fake.processes, 102)
		}
	}
	tree := fake.tree()
	if err := tree.Kill(); err != nil {
		t.Fatal(err)
	}
	if len(fake.kills) != 2 || fake.kills[0] != 101 || fake.kills[1] != 100 {
		t.Fatalf("kills after member exit = %v, want live job group then shell group", fake.kills)
	}
}

func TestUnixSessionKillRejectsUnconfirmedLeader(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{200: 200}}
	tree := fake.tree()
	if err := tree.Kill(); err == nil {
		t.Fatal("cleanup without a verifiable session leader reported success")
	}
	if len(fake.kills) != 0 {
		t.Fatalf("kills without a verifiable leader = %v, want none", fake.kills)
	}
}

func TestUnixSessionKillChecksProcessGroupBeforeKill(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100, 200: 200}}
	api := fake.api()
	api.getProcessGroupID = func(pid int) (int, error) {
		if pid == 200 {
			return 201, nil
		}
		return fake.getProcessGroupID(pid)
	}
	tree := fake.treeWithAPI(api)
	if err := tree.Kill(); err == nil {
		t.Fatal("cleanup with changed PGID reported success")
	}
	if len(fake.kills) != 0 {
		t.Fatalf("kills with changed PGID = %v, want none", fake.kills)
	}
}

func TestUnixSessionKillRejectsReusedLeaderIdentity(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100, 200: 200}, identity: "old-leader"}
	tree := fake.tree()
	fake.identity = "new-leader"
	if err := tree.Kill(); err == nil {
		t.Fatal("cleanup with a reused leader identity reported success")
	}
	if len(fake.kills) != 0 {
		t.Fatalf("kills with a reused leader identity = %v, want none", fake.kills)
	}
}

func TestUnixSessionKillReportsIdentityReadFailure(t *testing.T) {
	injected := errors.New("injected identity read failure")
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100}, identityErr: injected}
	tree := fake.tree()
	if err := tree.Kill(); !errors.Is(err, injected) {
		t.Fatalf("identity read failure = %v, want injected error", err)
	}
	if len(fake.kills) != 0 {
		t.Fatalf("kills after identity read failure = %v, want none", fake.kills)
	}
}

func TestUnixSessionKillDoesNotKillReusedGroupWhenEmpty(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{500: 100}}
	api := fake.api()
	api.getSessionID = func(int) (int, error) { return 999, nil }
	tree := fake.treeWithAPI(api)
	if err := tree.Kill(); err != nil {
		t.Fatal(err)
	}
	if len(fake.kills) != 0 {
		t.Fatalf("kills with no target SID members = %v, want none", fake.kills)
	}
}

func TestUnixNonSessionKillKeepsProcessGroupBehavior(t *testing.T) {
	fake := &fakeUnixSession{sessionID: 100, processes: map[int]int{100: 100}}
	snapshots := 0
	api := fake.api()
	api.snapshot = func() ([]byte, error) {
		snapshots++
		return fake.snapshot()
	}
	tree := &unixProcessTree{process: &os.Process{Pid: 100}, api: api}
	if err := tree.Kill(); err != nil {
		t.Fatal(err)
	}
	if len(fake.kills) != 1 || fake.kills[0] != 100 || snapshots != 0 {
		t.Fatalf("non-session kill: kills=%v snapshots=%d", fake.kills, snapshots)
	}
}

func TestSessionRecordsProcessStartIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process start identity test requires Darwin or Linux")
	}
	session, err := Start(context.Background(), Config{Path: "/bin/sh", Args: []string{"-lc", "sleep 1000"}, Env: testEnvironment(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tree := session.terminal.(*unixTerminal).process.tree.(*unixProcessTree)
	if tree.identity == "" {
		t.Fatal("PTY leader start identity was not recorded")
	}
	current, err := readUnixProcessIdentity(tree.process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if current != tree.identity {
		t.Fatalf("recorded identity = %q, current = %q", tree.identity, current)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartRejectsIdentityReadFailure(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process start identity test requires Darwin or Linux")
	}
	injected := errors.New("injected initial identity read failure")
	api := defaultUnixProcessAPI()
	var leaderPID atomic.Int32
	api.readProcessIdentity = func(pid int) (string, error) {
		leaderPID.Store(int32(pid))
		return "", injected
	}
	processFactory := func(cmd *osexec.Cmd) (*Process, error) {
		return newStartedProcessWithAPI(cmd, api)
	}
	if _, err := Start(context.Background(), Config{Path: "/bin/sh", Args: []string{"-lc", "sleep 1000"}, Env: testEnvironment(), Cols: 80, Rows: 24, processFactory: processFactory}); !errors.Is(err, injected) {
		t.Fatalf("Start error = %v, want initial identity failure", err)
	}
	if leaderPID.Load() == 0 {
		t.Fatal("identity reader was not called during OpenPTY")
	}
	assertSessionProcessGone(t, int(leaderPID.Load()))
}

type retryProcessTree struct {
	failures int
	err      error
	kills    int
	closes   int
}

func (t *retryProcessTree) Kill() error {
	t.kills++
	if t.failures > 0 {
		t.failures--
		return t.err
	}
	return nil
}
func (t *retryProcessTree) Close() error { t.closes++; return nil }

func TestProcessCleanupRetriesFailures(t *testing.T) {
	injected := errors.New("injected process cleanup failure")
	tree := &retryProcessTree{failures: 2, err: injected}
	process := newProcess(tree, func() error { return nil })
	<-process.done
	if !errors.Is(process.CleanupError(), injected) {
		t.Fatalf("initial CleanupError = %v, want injected failure", process.CleanupError())
	}
	if err := process.Kill(); !errors.Is(err, injected) {
		t.Fatalf("second cleanup error = %v, want injected failure", err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("third cleanup error = %v", err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("successful cleanup was not idempotent: %v", err)
	}
	if tree.kills != 3 || tree.closes != 3 {
		t.Fatalf("cleanup calls: Kill=%d Close=%d, want 3 each", tree.kills, tree.closes)
	}
}

type retryTerminal struct {
	failures  int
	err       error
	killCalls int
}

func (t *retryTerminal) Read([]byte) (int, error)       { return 0, io.EOF }
func (t *retryTerminal) Write(data []byte) (int, error) { return len(data), nil }
func (t *retryTerminal) Close() error                   { return nil }
func (t *retryTerminal) Resize(uint32, uint32) error    { return nil }
func (t *retryTerminal) Wait(context.Context) error     { return nil }
func (t *retryTerminal) CleanupError() error            { return nil }
func (t *retryTerminal) Kill() error {
	t.killCalls++
	if t.failures > 0 {
		t.failures--
		return t.err
	}
	return nil
}

func TestSessionCloseRetriesFailedCleanup(t *testing.T) {
	injected := errors.New("injected scan failure")
	terminal := &retryTerminal{failures: 2, err: injected}
	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{terminal: terminal, ctx: ctx, cancel: cancel}
	if err := session.Close(); !errors.Is(err, injected) {
		t.Fatalf("first Close error = %v, want injected failure", err)
	}
	if err := session.Close(); !errors.Is(err, injected) {
		t.Fatalf("second Close error = %v, want injected failure", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("third Close error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent Close error = %v", err)
	}
	if terminal.killCalls != 3 {
		t.Fatalf("Kill calls = %d, want two failures and one retry", terminal.killCalls)
	}
}

func TestSessionCloseRetriesProcessSnapshotFailure(t *testing.T) {
	injected := errors.New("injected process snapshot failure")
	api := defaultUnixProcessAPI()
	snapshot := api.snapshot
	var calls atomic.Int32
	api.snapshot = func() ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, injected
		}
		return snapshot()
	}
	processFactory := func(cmd *osexec.Cmd) (*Process, error) {
		return newStartedProcessWithAPI(cmd, api)
	}
	session, err := Start(context.Background(), Config{Path: "/bin/sh", Args: []string{"-lc", "sleep 1000"}, Env: testEnvironment(), Cols: 80, Rows: 24, processFactory: processFactory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.Close(); !errors.Is(err, injected) {
		t.Fatalf("first Close error = %v, want snapshot failure", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("retry after snapshot failure = %v", err)
	}
}

func TestSessionWaitPreservesDeadlineCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	session, err := Start(ctx, Config{Path: "/bin/sh", Args: []string{"-lc", "sleep 1000"}, Env: testEnvironment(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Wait(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestSessionRejectsInvalidDimensions(t *testing.T) {
	for _, size := range [][2]uint32{{0, 1025}, {1025, 24}} {
		if _, err := Start(context.Background(), Config{Path: "/bin/sh", Cols: size[0], Rows: size[1]}); err == nil {
			t.Fatalf("size %v unexpectedly succeeded", size)
		}
	}
}

func testEnvironment() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "TERM=xterm-256color"}
}

func testShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func readSessionPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PID file %s was not created", path)
	return 0
}

func assertSessionProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	pidText := strconv.Itoa(pid)
	for time.Now().Before(deadline) {
		output, err := osexec.Command("ps", "-A", "-o", "pid=,stat=").Output()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != pidText {
				continue
			}
			if len(fields) != 2 {
				t.Fatalf("unexpected process list line %q", line)
			}
			found = true
			if fields[1][0] == 'Z' {
				return
			}
			break
		}
		if !found {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive", pid)
}
