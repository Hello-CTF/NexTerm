//go:build unix

package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestExecDrainsStdoutAndStderrConcurrently(t *testing.T) {
	transport := unixTransport(t)
	result, err := transport.Exec(context.Background(), "yes x | head -c 200000; yes e | head -c 200000 >&2", base.ExecOptions{
		Timeout: 10 * time.Second,
		Limits:  base.OutputLimits{Stdout: -1, Stderr: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || len(result.Stdout) != 200000 || len(result.Stderr) != 200000 || result.Truncated {
		t.Fatalf("unexpected result: code=%v stdout=%d stderr=%d truncated=%v", result.ExitCode, len(result.Stdout), len(result.Stderr), result.Truncated)
	}
}

func TestExecReturnsExitStatusAndExactLimits(t *testing.T) {
	transport := unixTransport(t)
	result, err := transport.Exec(context.Background(), "printf 1234567890; printf abcde >&2; exit 7", base.ExecOptions{
		Timeout: 5 * time.Second,
		Limits:  base.OutputLimits{Stdout: 4, Stderr: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "1234" || result.Stderr != "ab" || !result.Truncated || result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecStdinIsClosed(t *testing.T) {
	transport := unixTransport(t)
	result, err := transport.Exec(context.Background(), "cat; printf done", base.ExecOptions{Timeout: 5 * time.Second})
	if err != nil || result.Stdout != "done" {
		t.Fatalf("Exec = %#v, %v", result, err)
	}
}

func TestExecTimeoutKillsProcessTree(t *testing.T) {
	transport := unixTransport(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	command := fmt.Sprintf("sleep 1000 & child=$!; printf '%%s' \"$child\" > %s; wait \"$child\"", shellQuote(pidPath))
	_, err := transport.Exec(context.Background(), command, base.ExecOptions{Timeout: 400 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	pid := readPID(t, pidPath)
	assertProcessGone(t, pid)
}

func TestExecParentExitStillCleansChildren(t *testing.T) {
	transport := unixTransport(t)
	pidPath := filepath.Join(t.TempDir(), "orphan.pid")
	command := fmt.Sprintf("sleep 1000 >/dev/null 2>&1 & child=$!; printf '%%s' \"$child\" > %s; exit 0", shellQuote(pidPath))
	result, err := transport.Exec(context.Background(), command, base.ExecOptions{Timeout: 5 * time.Second})
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("Exec = %#v, %v", result, err)
	}
	assertProcessGone(t, readPID(t, pidPath))
}

func TestExecParentExitClosesInheritedPipes(t *testing.T) {
	transport := unixTransport(t)
	pidPath := filepath.Join(t.TempDir(), "pipe-child.pid")
	command := fmt.Sprintf("sleep 1000 & child=$!; printf '%%s' \"$child\" > %s; exit 0", shellQuote(pidPath))
	result, err := transport.Exec(context.Background(), command, base.ExecOptions{Timeout: 5 * time.Second})
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("Exec = %#v, %v", result, err)
	}
	assertProcessGone(t, readPID(t, pidPath))
}

func TestExecContextCancellation(t *testing.T) {
	transport := unixTransport(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := transport.Exec(ctx, "sleep 1000", base.ExecOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestOpenExecStreamsAndCloseWrite(t *testing.T) {
	transport := unixTransport(t)
	channel, err := transport.OpenExec(context.Background(), "cat; printf ':done'", base.ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if _, err := channel.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := channel.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(channel)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "hello:done" {
		t.Fatalf("output = %q", output)
	}
	if err := channel.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channel.Resize(context.Background(), 80, 24); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("Resize error = %v", err)
	}
}

func TestTransportCloseAndGeneration(t *testing.T) {
	transport := unixTransport(t)
	if _, err := transport.OpenExec(context.Background(), ":", base.ExecOptions{ExpectedGeneration: transport.Generation() + 1}); !errors.Is(err, base.ErrStaleGeneration) {
		t.Fatalf("stale generation error = %v", err)
	}
	channel, err := transport.OpenExec(context.Background(), "sleep 1000", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.IsAlive() {
		t.Fatal("transport remains alive after Close")
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Exec(context.Background(), ":", base.ExecOptions{}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("closed transport error = %v", err)
	}
}

func unixTransport(t *testing.T) *Transport {
	t.Helper()
	return NewWithConfig(Config{Shell: "/bin/sh", CWD: t.TempDir(), Env: map[string]string{"LANG": "C.UTF-8"}})
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func readPID(t *testing.T, path string) int {
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

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive", pid)
}
