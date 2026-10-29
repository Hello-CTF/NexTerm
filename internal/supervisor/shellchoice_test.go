//go:build unix

package supervisor

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func fakeShell(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultShellCommandPrefersAccountShellOverInheritedSHELL(t *testing.T) {
	account := fakeShell(t, "zsh")
	inherited := fakeShell(t, "bash")
	pinAccountShell(t, account)
	t.Setenv("SHELL", inherited)
	if got := defaultShellCommand([]string{"PATH=/usr/bin"}); !slices.Equal(got, []string{account, "-l"}) {
		t.Fatalf("defaultShellCommand = %v, want the account shell %v", got, account)
	}
}

func TestDefaultShellCommandUsesInheritedSHELLOnlyAsLastFallback(t *testing.T) {
	inherited := fakeShell(t, "bash")
	pinAccountShell(t, "")
	t.Setenv("SHELL", inherited)
	if got := defaultShellCommand(nil); !slices.Equal(got, []string{inherited, "-l"}) {
		t.Fatalf("defaultShellCommand = %v, want inherited %v", got, inherited)
	}
}

func TestDefaultShellCommandFallbackIsNotALoginShell(t *testing.T) {
	pinAccountShell(t, "")
	t.Setenv("SHELL", "")
	if err := os.Unsetenv("SHELL"); err != nil {
		t.Fatal(err)
	}
	if got := defaultShellCommand(nil); !slices.Equal(got, []string{"/bin/sh"}) {
		t.Fatalf("defaultShellCommand without any shell = %v, want [/bin/sh]", got)
	}
}

func TestDefaultShellCommandSessionEnvironmentShellWins(t *testing.T) {
	requested := fakeShell(t, "fish")
	pinAccountShell(t, fakeShell(t, "zsh"))
	if got := defaultShellCommand([]string{"SHELL=" + requested}); !slices.Equal(got, []string{requested, "-l"}) {
		t.Fatalf("defaultShellCommand = %v, want requested %v", got, requested)
	}
}
