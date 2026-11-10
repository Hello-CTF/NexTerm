//go:build unix

package local

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestShellArgsStartInteractiveAndDurableShell(t *testing.T) {
	transport := NewWithConfig(Config{Shell: "/bin/sh", ShellArgs: []string{"-c", "printf 'args-%s\\n' ok"}, CWD: t.TempDir()})
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := transport.OpenPTY(ctx, base.PTYOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	output, err := io.ReadAll(channel)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "args-ok") {
		t.Fatalf("PTY output = %q, want the shell to run its configured arguments", output)
	}
	command, _, _ := transport.DurableLaunch()
	if want := []string{"/bin/sh", "-c", "printf 'args-%s\\n' ok"}; !reflect.DeepEqual(command, want) {
		t.Fatalf("DurableLaunch command = %q, want %q", command, want)
	}
	result, err := transport.Exec(ctx, "printf exec-ok", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "exec-ok" {
		t.Fatalf("exec stdout = %q, want the command run without the interactive arguments", result.Stdout)
	}
}
