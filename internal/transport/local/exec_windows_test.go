//go:build windows

package local

import (
	"context"
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
