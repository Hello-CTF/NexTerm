//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	helperDirEnv    = "NEXTERM_SUPERVISOR_HELPER_DIR"
	helperSocketEnv = "NEXTERM_SUPERVISOR_HELPER_SOCKET"
)

func TestSupervisorHelperProcess(t *testing.T) {
	dir := os.Getenv(helperDirEnv)
	if dir == "" {
		t.Skip("supervisor helper process")
	}
	supervisor, err := New(Config{StateDir: dir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(supervisor, os.Getenv(helperSocketEnv))
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	<-signals
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	_ = supervisor.Close()
}

func startHelperSupervisor(t *testing.T, dir string) (*exec.Cmd, string) {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSupervisorHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(), helperDirEnv+"="+dir, helperSocketEnv+"="+socketPath)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	client := NewClient(socketPath, dir)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := client.List(context.Background()); err == nil {
			return cmd, socketPath
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper supervisor did not become ready: %s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRegistryRecoveryAfterSupervisorCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	cmd, socketPath := startHelperSupervisor(t, dir)
	client := NewClient(socketPath, dir)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `printf 'crash-marker\n'; sleep 60`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "crash-marker")
	_ = stream.Close()

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	state, err := cmd.Process.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode() != -1 {
		t.Fatalf("helper exit code = %d, want -1 (killed)", state.ExitCode())
	}

	recovered, err := New(Config{StateDir: dir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Close() }()
	infos, err := recovered.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("recovered sessions = %+v, want exactly one", infos)
	}
	info := infos[0]
	if info.ID != id || !info.Dead {
		t.Fatalf("recovered info = %+v", info)
	}
	if info.ExitCode != nil || info.Signal != "" {
		t.Fatalf("crash-recovered exit status = %+v, want unknown", info)
	}

	attachment, err := recovered.Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	output := string(readToEOF(t, attachment))
	if !strings.Contains(output, "crash-marker") {
		t.Fatalf("crash recovery replay = %q", output)
	}
	if err := attachment.Wait(ctx); err != nil {
		t.Fatalf("Wait on crash-recovered session = %v, want nil (unknown status)", err)
	}
	if err := recovered.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Attach(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach after recovery kill = %v, want ErrNotFound", err)
	}
}

func TestRegistryRecoveryAfterHelperGracefulStop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	cmd, socketPath := startHelperSupervisor(t, dir)
	client := NewClient(socketPath, dir)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `printf 'graceful-marker\n'; sleep 60`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "graceful-marker")
	_ = stream.Close()

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("helper graceful stop: %v", err)
	}

	recovered, err := New(Config{StateDir: dir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Close() }()
	infos, err := recovered.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || !infos[0].Dead {
		t.Fatalf("recovered sessions = %+v", infos)
	}
	info := infos[0]
	if info.ExitCode == nil || *info.ExitCode != -1 || info.Signal != "SIGKILL" {
		t.Fatalf("graceful-stop exit status = %+v, want SIGKILL", info)
	}

	attachment, err := recovered.Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	output := string(readToEOF(t, attachment))
	if !strings.Contains(output, "graceful-marker") {
		t.Fatalf("recovery replay = %q", output)
	}
	err = attachment.Wait(ctx)
	var exitErr *base.ExitError
	if !errors.As(err, &exitErr) || exitErr.Signal != "SIGKILL" {
		t.Fatalf("recovered Wait = %v, want SIGKILL exit error", err)
	}
}
