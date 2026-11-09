//go:build windows

package pty

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestStartProcessInheritsOnlyItsStandardHandles(t *testing.T) {
	// os.Pipe handles are inheritable, as are those of any concurrent start that has not closed its child
	// ends yet; a sibling started meanwhile must not keep this write end open.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdoutR.Close()
	sibling := startWindowsTestProcess(t, "Start-Sleep -Seconds 20")
	defer func() { _ = sibling.Kill() }()
	if err := stdoutW.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(stdoutR)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pipe did not reach EOF: a sibling process inherited its write end")
	}
}

func TestConPTYCloseWhileExecAlive(t *testing.T) {
	session := startWindowsTestSession(t, context.Background(), "while ($true) { [Console]::Out.Write('x' * 65536); Start-Sleep -Milliseconds 50 }")
	closeStarted := false
	defer func() {
		if !closeStarted {
			_ = session.Close()
		}
	}()
	// Started after the pseudoconsole exists, so an inheritable master end would leak into it.
	sibling := startWindowsTestProcess(t, "Start-Sleep -Seconds 20")
	defer func() { _ = sibling.Kill() }()
	closeStarted = true
	closeWindowsSessionWithin(t, session)
}

func startWindowsTestProcess(t *testing.T, command string) *Process {
	t.Helper()
	var parentEnds, childEnds []*os.File
	defer func() {
		for _, file := range childEnds {
			_ = file.Close()
		}
	}()
	t.Cleanup(func() {
		for _, file := range parentEnds {
			_ = file.Close()
		}
	})
	pipe := func(childReads bool) *os.File {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if childReads {
			parentEnds, childEnds = append(parentEnds, writer), append(childEnds, reader)
			return reader
		}
		parentEnds, childEnds = append(parentEnds, reader), append(childEnds, writer)
		return writer
	}
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-Command", command)
	cmd.Stdin = pipe(true)
	cmd.Stdout = pipe(false)
	cmd.Stderr = pipe(false)
	process, err := StartProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Kill()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = process.Wait(ctx)
	})
	return process
}
