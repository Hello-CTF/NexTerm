//go:build unix

package pty

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

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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
