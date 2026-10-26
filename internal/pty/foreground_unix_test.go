//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package pty

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"
)

// TestForegroundReportsJobAndLineMode starts a shell that runs a job in its
// own process group and switches the line discipline, and checks what the
// master side reports at each step.
func TestForegroundReportsJobAndLineMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := Start(ctx, Config{
		Path: "/bin/sh",
		Args: []string{"-c", `set -m; echo leader-ready; read a; stty -icanon; echo raw-ready; read b; sh -c 'echo job-ready; read c'; read d`},
		Env:  testEnvironment(),
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	lines := bufio.NewReader(session)
	await := func(marker string) {
		t.Helper()
		for {
			line, err := lines.ReadString('\n')
			if strings.Contains(line, marker) {
				return
			}
			if err != nil {
				t.Fatalf("waiting for %q: %v", marker, err)
			}
		}
	}
	// Each marker is printed after the state it announces is in place: stty
	// returns before the echo, and the job holds the foreground before it
	// runs.
	await("leader-ready")
	fg, err := session.Foreground()
	if err != nil {
		t.Fatal(err)
	}
	if !fg.LeaderInForeground() || !fg.CanonicalKnown || !fg.Canonical {
		t.Fatalf("leader reading a line: %+v", fg)
	}

	if _, err := session.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	await("raw-ready")
	if fg, err = session.Foreground(); err != nil || !fg.CanonicalKnown || fg.Canonical {
		t.Fatalf("after stty -icanon: %+v, %v", fg, err)
	}

	if _, err := session.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	await("job-ready")
	if fg, err = session.Foreground(); err != nil || fg.LeaderInForeground() || fg.ProcessGroup <= 0 {
		t.Fatalf("job in the foreground: %+v, %v", fg, err)
	}
}
