//go:build windows

package pty

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSessionWindowsNaturalExitEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := Start(ctx, Config{
		Path: "powershell.exe",
		Args: []string{"-NoLogo", "-NoProfile", "-Command", "[Console]::Out.Write('pty-ok'); exit 0"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "pty-ok") {
		t.Fatalf("output = %q", output)
	}
	if err := session.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionWindowsCloseWithoutReader(t *testing.T) {
	session, err := Start(context.Background(), Config{
		Path: "powershell.exe",
		Args: []string{"-NoLogo", "-NoProfile", "-Command", "while ($true) { [Console]::Out.Write('x' * 65536); Start-Sleep -Milliseconds 50 }"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not finish without a reader")
	}
}
