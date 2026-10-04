//go:build unix

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"golang.org/x/sys/unix"
)

const (
	helperAppDirEnv   = "NEXTERM_SUPERVISOR_HELPER_APP_DIR"
	helperAppReadyEnv = "NEXTERM_SUPERVISOR_HELPER_APP_READY"
	spawnChildFileEnv = "NEXTERM_SUPERVISOR_SPAWN_CHILD_FILE"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperCommand {
		os.Exit(RunHelperCLI(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestHelperSessionsSurviveAppProcessExitAndReattach(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	readyFile := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDesktopHelperAppProcess$", "-test.v")
	cmd.Env = append(os.Environ(), helperAppDirEnv+"="+stateDir, helperAppReadyEnv+"="+readyFile)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("app process never became ready: %s", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("app process exit: %v\n%s", err, output.String())
	}
	sessionID, err := os.ReadFile(readyFile)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	restarted, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Spawned() {
		t.Fatal("app restart spawned a second helper instead of reattaching")
	}
	infos, err := restarted.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != string(sessionID) || infos[0].Dead {
		t.Fatalf("sessions after app exit = %+v", infos)
	}

	attachment, err := NewRemoteProvider(restarted.Client()).Attach(ctx, string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	replayed := readUntil(t, attachment, "app-input-marker")
	if count := countOccurrences(string(replayed), "app-boot-marker"); count != 1 {
		t.Fatalf("boot marker replayed %d times: %q", count, replayed)
	}
	if count := countOccurrences(string(replayed), "app-input-marker"); count != 1 {
		t.Fatalf("input marker replayed %d times: %q", count, replayed)
	}
	if _, err := attachment.Write([]byte("printf 'post-restart-marker\\n'\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "post-restart-marker")
	if err := attachment.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	infos, err = restarted.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Cols != 100 || infos[0].Rows != 30 {
		t.Fatalf("resized session grid = %+v, want 100x30", infos)
	}
	if err := attachment.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	infos, err = restarted.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("session survived kill: %+v", infos)
	}
}

func TestDesktopHelperAppProcess(t *testing.T) {
	stateDir := os.Getenv(helperAppDirEnv)
	if stateDir == "" {
		t.Skip("desktop helper app process")
	}
	ctx := context.Background()
	helper, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := NewRemoteProvider(helper.Client()).Create(ctx, base.DurableCreateOptions{
		ID:      ids.New(),
		Command: []string{"/bin/sh", "-c", "printf 'app-boot-marker\\n'; sleep 300"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "app-boot-marker")
	if _, err := attachment.Write([]byte("stty -echo\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte("printf 'app-input-marker\\n'\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "app-input-marker")
	if err := os.WriteFile(os.Getenv(helperAppReadyEnv), []byte(attachment.ID()), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestHelperSessionsSurviveClientRestart(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	first, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Spawned() {
		t.Fatal("first connect did not spawn the helper")
	}
	id := ids.New()
	attachment, err := NewRemoteProvider(first.Client()).Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "printf 'client-boot-marker\\n'; sleep 300"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "client-boot-marker")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if second.Spawned() {
		t.Fatal("second connect spawned a new helper")
	}
	infos, err := second.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || infos[0].Dead {
		t.Fatalf("sessions after client close = %+v", infos)
	}
	reattached, err := NewRemoteProvider(second.Client()).Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	replayed := readUntil(t, reattached, "client-boot-marker")
	if count := countOccurrences(string(replayed), "client-boot-marker"); count != 1 {
		t.Fatalf("boot marker replayed %d times: %q", count, replayed)
	}
	if err := reattached.Kill(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHelperRespawnsAfterHelperCrashWithoutSurvivalClaim(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	first, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	id := ids.New()
	attachment, err := NewRemoteProvider(first.Client()).Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "printf 'crash-boot-marker\\n'; sleep 60"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "crash-boot-marker")
	_ = attachment.Close()

	killHelperProcesses(t, stateDir)
	waitForHelperExit(t, stateDir)

	second, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Spawned() {
		t.Fatal("connect after a helper crash did not spawn a replacement")
	}
	infos, err := second.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || !infos[0].Dead {
		t.Fatalf("crash-recovered sessions = %+v, want one dead session without survival claims", infos)
	}
	if infos[0].ExitCode != nil || infos[0].Signal != "" {
		t.Fatalf("crash-recovered exit status = %+v, want unknown", infos[0])
	}
	reattached, err := NewRemoteProvider(second.Client()).Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	readUntil(t, reattached, "crash-boot-marker")
	if err := reattached.Wait(ctx); err != nil {
		t.Fatalf("Wait on a crash-recovered session = %v, want nil (unknown status)", err)
	}
	if err := reattached.Kill(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConnectHelperProtocolMismatchFailsWithoutSpawnOrKill(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	endpoint := helperEndpoint(stateDir)
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	var connections atomic.Int32
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			kind, _, err := readFrame(conn)
			if err != nil || kind != frameHello {
				_ = conn.Close()
				continue
			}
			_ = writeFrame(conn, frameError, []byte(`{"code":"version_mismatch","message":"protocol version 99 is not supported"}`))
			kind, _, err = readFrame(conn)
			if err == nil && kind == frameKillSession {
				t.Error("protocol mismatch probe sent a kill frame")
			}
			_ = conn.Close()
		}
	}()

	_, err = ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, SpawnTimeout: 5 * time.Second})
	if err == nil {
		t.Fatal("connect to a wrong-version helper succeeded")
	}
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("connect error = %v, want a protocol error", err)
	}
	if !strings.Contains(err.Error(), endpoint) {
		t.Fatalf("connect error %q does not identify the endpoint %s", err, endpoint)
	}
	time.Sleep(300 * time.Millisecond)
	if count := connections.Load(); count != 1 {
		t.Fatalf("fake helper saw %d connections, want exactly one probe (no spawn)", count)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "helper.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protocol mismatch spawned a helper (log present): %v", err)
	}
	_ = listener.Close()
	<-serverDone
}

func TestConnectHelperConcurrentSingleHelper(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	const clients = 8
	helpers := make([]*Helper, clients)
	errs := make([]error, clients)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for index := 0; index < clients; index++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			helpers[index], errs[index] = ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, SpawnTimeout: 60 * time.Second})
		}(index)
	}
	start.Done()
	done.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("connect %d: %v", index, err)
		}
	}
	endpoint := helpers[0].Endpoint()
	for _, helper := range helpers {
		if helper.Endpoint() != endpoint {
			t.Fatalf("helper endpoints differ: %s vs %s", helper.Endpoint(), endpoint)
		}
	}
	id := ids.New()
	if _, err := helpers[0].Client().Create(context.Background(), CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 300"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	infos, err := helpers[clients-1].Client().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("concurrent helper list = %+v", infos)
	}
	if count := helperProcessCount(t, stateDir); count != 1 {
		t.Fatalf("helper processes = %d, want exactly 1", count)
	}
	if err := helpers[0].Client().Kill(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnDetachedStartsNewSession(t *testing.T) {
	sidFile := filepath.Join(t.TempDir(), "sid")
	t.Setenv(spawnChildFileEnv, sidFile)
	logPath := filepath.Join(t.TempDir(), "spawn.log")
	if err := spawnDetached(os.Args[0], []string{"-test.run=^TestSpawnDetachedChild$"}, logPath); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(sidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("spawned child never wrote its session: %s", data)
		}
		time.Sleep(25 * time.Millisecond)
	}
	data, err := os.ReadFile(sidFile)
	if err != nil {
		t.Fatal(err)
	}
	childSID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	parentSID, err := unix.Getsid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if childSID == parentSID {
		t.Fatalf("spawned child shares the parent session %d; want a detached session", childSID)
	}
}

func TestSpawnDetachedChild(t *testing.T) {
	path := os.Getenv(spawnChildFileEnv)
	if path == "" {
		t.Skip("spawn detached child")
	}
	sid, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(sid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLockHelperStateExclusive(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockHelperState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockHelperState(stateDir); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second lock = %v, want ErrAlreadyExists", err)
	}
	unlock()
	second, err := lockHelperState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	second()
}

func TestHelperEndpointIsVersionedAndPrivate(t *testing.T) {
	stateDir := t.TempDir()
	endpoint, err := HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(endpoint, fmt.Sprintf("supervisor-v%d.sock", ProtocolVersion)) {
		t.Fatalf("endpoint %q is not versioned", endpoint)
	}
	if !strings.HasPrefix(endpoint, os.TempDir()) {
		t.Fatalf("endpoint %q is not under the user runtime directory", endpoint)
	}
	other, err := HelperEndpoint(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if endpoint == other {
		t.Fatalf("endpoint %q is not scoped to its state directory", endpoint)
	}
}

func TestParseHelperArgs(t *testing.T) {
	config, err := parseHelperArgs([]string{"--state-dir", "/custom/state"}, nil)
	if err != nil || config.StateDir != "/custom/state" {
		t.Fatalf("explicit state dir = %+v, %v", config, err)
	}
	config, err = parseHelperArgs([]string{"--data-dir=/data"}, nil)
	if err != nil || config.StateDir != filepath.Join("/data", "durable", "supervisor") {
		t.Fatalf("derived state dir = %+v, %v", config, err)
	}
	config, err = parseHelperArgs(nil, func(string) string { return "/env/data" })
	if err != nil || config.StateDir != filepath.Join("/env/data", "durable", "supervisor") {
		t.Fatalf("env state dir = %+v, %v", config, err)
	}
	if _, err := parseHelperArgs(nil, nil); err == nil {
		t.Fatal("missing directories accepted")
	}
	if _, err := parseHelperArgs([]string{"--bogus", "x"}, nil); err == nil {
		t.Fatal("unknown option accepted")
	}
	if _, err := parseHelperArgs([]string{"--state-dir"}, nil); err == nil {
		t.Fatal("missing option value accepted")
	}
	if _, err := parseHelperArgs([]string{"stray"}, nil); err == nil {
		t.Fatal("stray argument accepted")
	}
}

func killHelperProcesses(t *testing.T, stateDir string) {
	t.Helper()
	cmd := exec.Command("pkill", "-9", "-f", HelperCommand+" --state-dir "+stateDir)
	_ = cmd.Run()
}

func helperProcessCount(t *testing.T, stateDir string) int {
	t.Helper()
	output, err := exec.Command("pgrep", "-f", HelperCommand+" --state-dir "+stateDir).Output()
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(output)))
}

func waitForHelperExit(t *testing.T, stateDir string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for helperProcessCount(t, stateDir) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("helper process did not exit")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
