//go:build unix

package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/shellenv"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func init() {
	// Tests must never spawn the developer's interactive login shell.
	shellenv.SetDefault(shellenv.New(shellenv.Options{Disabled: true}))
}

func pinAccountShell(t *testing.T, shell string) {
	t.Helper()
	previous := shellenv.Default()
	shellenv.SetDefault(shellenv.New(shellenv.Options{Disabled: true, Account: func() string { return shell }}))
	t.Cleanup(func() { shellenv.SetDefault(previous) })
}

func fakeShell(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultShellPrefersAccountShellOverInheritedSHELL(t *testing.T) {
	account := fakeShell(t, "zsh")
	pinAccountShell(t, account)
	t.Setenv("SHELL", fakeShell(t, "bash"))
	shell, login, source, _ := defaultShellChoice()
	if shell != account || !login || source != "account" {
		t.Fatalf("defaultShellChoice = (%q, %v, %q), want the account shell", shell, login, source)
	}
}

func TestDefaultShellInheritedSHELLIsLastFallback(t *testing.T) {
	inherited := fakeShell(t, "bash")
	pinAccountShell(t, "")
	t.Setenv("SHELL", inherited)
	if shell, login, source, _ := defaultShellChoice(); shell != inherited || !login || source != "env" {
		t.Fatalf("defaultShellChoice = (%q, %v, %q)", shell, login, source)
	}
}

func TestDefaultShellFallbackIsNotALoginShell(t *testing.T) {
	pinAccountShell(t, "")
	t.Setenv("SHELL", "")
	if err := os.Unsetenv("SHELL"); err != nil {
		t.Fatal(err)
	}
	shell, login := defaultShell()
	if shell != "/bin/sh" || login {
		t.Fatalf("defaultShell without any shell = (%q, %v)", shell, login)
	}
	transport := NewWithConfig(Config{CWD: t.TempDir()})
	if transport.loginShell {
		t.Fatal("fallback transport must not use login-shell flags")
	}
	result, err := transport.Exec(context.Background(), "printf fallback-ok", base.ExecOptions{Timeout: 5 * time.Second})
	if err != nil || result.Stdout != "fallback-ok" {
		t.Fatalf("Exec via fallback shell = %#v, %v", result, err)
	}
}

func TestAssetShellOptionIsReportedAsSource(t *testing.T) {
	pinAccountShell(t, fakeShell(t, "zsh"))
	transport := NewWithConfig(Config{Shell: "/bin/bash", CWD: t.TempDir()})
	if path, source, _ := transport.ShellInfo(); path != "/bin/bash" || source != "asset option" {
		t.Fatalf("ShellInfo = (%q, %q), want the asset option to be visible as the cause", path, source)
	}
}

func TestDurableLaunchPassesShellAndPathExplicitly(t *testing.T) {
	account := fakeShell(t, "zsh")
	pinAccountShell(t, account)
	t.Setenv("PATH", "/fresh/bin:/usr/bin")
	transport := NewWithConfig(Config{CWD: t.TempDir()})
	command, _, env := transport.DurableLaunch()
	if len(command) != 1 || command[0] != account {
		t.Fatalf("command = %v, want the account shell", command)
	}
	if got := environmentValue(env, "SHELL"); got != account {
		t.Fatalf("SHELL = %q: a stale helper environment would otherwise decide", got)
	}
	if got := environmentValue(env, "PATH"); got != "/fresh/bin:/usr/bin" {
		t.Fatalf("PATH = %q", got)
	}
}

func TestTerminalEnvironmentReplacesInheritedShell(t *testing.T) {
	account := fakeShell(t, "zsh")
	t.Setenv("SHELL", "/bin/bash")
	env := environmentFor(account, nil, true, "")
	if got := environmentValue(env, "SHELL"); got != account {
		t.Fatalf("SHELL = %q, want %q", got, account)
	}
}
