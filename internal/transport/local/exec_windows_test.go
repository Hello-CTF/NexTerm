//go:build windows

package local

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestExecPowerShell(t *testing.T) {
	transport := NewWithConfig(Config{Shell: "powershell.exe", CWD: t.TempDir()})
	result, err := transport.Exec(context.Background(), "[Console]::Out.Write('ok'); [Console]::Error.Write('problem'); exit 3", base.ExecOptions{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "ok" || result.Stderr != "problem" || result.ExitCode == nil || *result.ExitCode != 3 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecIgnoresInteractiveCmdShell(t *testing.T) {
	transport := NewWithConfig(Config{Shell: "cmd.exe", CWD: t.TempDir()})
	defer transport.Close()
	result, err := transport.Exec(context.Background(), "[Console]::Out.Write('ps-' + $PSVersionTable.PSVersion.Major.GetType().Name)", base.ExecOptions{Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "ps-Int32" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("exec with a cmd.exe interactive shell did not run PowerShell: %#v", result)
	}
}

func TestConcurrentExecDoesNotWaitForSibling(t *testing.T) {
	transport := NewWithConfig(Config{Shell: "powershell.exe", CWD: t.TempDir()})
	defer transport.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const pairs = 3
	start := make(chan struct{})
	var slow sync.WaitGroup
	fast := make(chan error, pairs)
	for range pairs {
		slow.Add(1)
		go func() {
			defer slow.Done()
			<-start
			_, _ = transport.Exec(ctx, "Start-Sleep -Seconds 20", base.ExecOptions{Timeout: 30 * time.Second})
		}()
		go func() {
			<-start
			started := time.Now()
			result, err := transport.Exec(ctx, "[Console]::Out.Write('ok')", base.ExecOptions{Timeout: 30 * time.Second})
			switch {
			case err != nil:
				fast <- err
			case result.Stdout != "ok":
				fast <- fmt.Errorf("fast exec stdout = %q", result.Stdout)
			case time.Since(started) > 10*time.Second:
				// A leaked pipe end keeps the fast exec open until a 20 second sibling exits.
				fast <- fmt.Errorf("fast exec took %s beside a sleeping sibling", time.Since(started))
			default:
				fast <- nil
			}
		}()
	}
	close(start)
	for range pairs {
		if err := <-fast; err != nil {
			t.Error(err)
		}
	}
	cancel()
	slow.Wait()
}
