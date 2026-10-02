package durable

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxDirectCommandSignals(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux direct-command signals skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux and /bin/kill)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	for _, test := range []struct {
		name   string
		status int
	}{{"int", 130}, {"term", 143}} {
		t.Run(test.name, func(t *testing.T) {
			backend, config, ctx := newRealSignalBackend(t, binary, "direct")
			id := ids.New()
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				_ = backend.Kill(cleanupCtx, id)
			})
			session, err := backend.Create(ctx, CreateOptions{ID: id, Command: []string{"/bin/sleep", "30"}})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "int" {
				if _, err := session.Write([]byte{0x03}); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = directChildPID(t, session.Info().PID)
				if err := signalProcessGroup(ctx, session.Info().PID, "-TERM"); err != nil {
					t.Fatal(err)
				}
			}
			output := readAllBounded(t, ctx, session)
			if test.name == "int" && !bytes.Contains(output, []byte("^C")) {
				t.Fatalf("SIGINT output = %q, want terminal ^C bytes", output)
			}
			if test.name == "term" && !bytes.Contains(output, []byte("Terminated: 15")) {
				t.Fatalf("SIGTERM output = %q, want signal completion bytes", output)
			}
			infos, err := backend.List(ctx)
			if err != nil || len(infos) != 1 || !infos[0].Dead || infos[0].ExitCode == nil || *infos[0].ExitCode != test.status {
				t.Fatalf("signal discovery = %+v, err=%v, want exit status %d", infos, err, test.status)
			}
			if err := session.Detach(); err != nil {
				t.Fatal(err)
			}
			backend, err = New(config)
			if err != nil {
				t.Fatal(err)
			}
			session, err = backend.Attach(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			replay := readAllBounded(t, ctx, session)
			if !bytes.Equal(replay, output) {
				t.Fatalf("signal replay = %q, want %q", replay, output)
			}
			if info := session.Info(); info.ExitCode == nil || *info.ExitCode != test.status {
				t.Fatalf("fresh signal info = %+v", info)
			}
			if err := session.Detach(); err != nil {
				t.Fatal(err)
			}
			if err := backend.Kill(ctx, id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRealTmuxInteractiveShellSignals(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux interactive-shell signals skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux and /bin/kill)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	backend, config, ctx := newRealSignalBackend(t, binary, "interactive")
	id := ids.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, id)
	})
	shellRC := filepath.Join(filepath.Dir(config.StateDir), "shellrc")
	if err := os.WriteFile(shellRC, []byte("PS1='nx$ '\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := backend.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-i"},
		Env:     []string{"ENV=" + shellRC},
	})
	if err != nil {
		t.Fatal(err)
	}
	var initial bytes.Buffer
	if err := readUntil(ctx, session, &initial, "nx$ "); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("/bin/sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	var sleeping bytes.Buffer
	if err := readUntil(ctx, session, &sleeping, "/bin/sleep 30\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	var interrupted bytes.Buffer
	if err := readUntil(ctx, session, &interrupted, "nx$ "); err != nil {
		t.Fatal(err)
	}
	infos, err := backend.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Dead {
		t.Fatalf("interactive shell died after Ctrl-C: infos=%+v err=%v", infos, err)
	}
	if _, err := session.Write([]byte("trap 'echo term-seen' TERM\r")); err != nil {
		t.Fatal(err)
	}
	var trapInstalled bytes.Buffer
	if err := readUntil(ctx, session, &trapInstalled, "nx$ "); err != nil {
		t.Fatal(err)
	}
	if err := signalProcess(ctx, directChildPID(t, session.Info().PID), "-TERM"); err != nil {
		t.Fatal(err)
	}
	var terminated bytes.Buffer
	if err := readUntil(ctx, session, &terminated, "term-seen\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("echo after-term\r")); err != nil {
		t.Fatal(err)
	}
	var after bytes.Buffer
	if err := readUntil(ctx, session, &after, "after-term\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	_ = readAllBounded(t, ctx, session)
	infos, err = backend.List(ctx)
	if err != nil || len(infos) != 1 || !infos[0].Dead || infos[0].ExitCode == nil || *infos[0].ExitCode != 0 || infos[0].Signal != "" {
		t.Fatalf("interactive exit discovery = %+v, err=%v", infos, err)
	}
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("interactive shell survived Ctrl-C, handled SIGTERM, accepted later input, and completed with status 0")
}

func newRealSignalBackend(t *testing.T, binary, kind string) (*Backend, Config, context.Context) {
	t.Helper()
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-durable-signal-"+kind+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	config := Config{
		Binary:         binary,
		SocketPath:     filepath.Join(runDir, "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "real-durable-signal-test",
		CommandTimeout: 3 * time.Second,
	}
	backend, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return backend, config, ctx
}

func directChildPID(t *testing.T, parent int) int {
	t.Helper()
	output, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 1 {
		t.Fatalf("supervisor children = %q, want one interactive shell", fields)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		t.Fatalf("invalid interactive shell PID %q", fields[0])
	}
	return pid
}

func signalProcessGroup(ctx context.Context, pgid int, signal string) error {
	cmd := exec.CommandContext(ctx, "/bin/kill", signal, "--", strconv.Itoa(-pgid))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kill %s process-group %d: %w: %s", signal, pgid, err, output)
	}
	return nil
}

func signalProcess(ctx context.Context, pid int, signal string) error {
	cmd := exec.CommandContext(ctx, "/bin/kill", signal, strconv.Itoa(pid))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kill %s %d: %w: %s", signal, pid, err, output)
	}
	return nil
}
