package main

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	fleetagent "github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == supervisor.HelperCommand {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestHelperProbeProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], supervisor.HelperCommand, "--probe")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper probe exit: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != strconv.Itoa(supervisor.ProtocolVersion) {
		t.Fatalf("helper probe stdout = %q, want protocol version %d", got, supervisor.ProtocolVersion)
	}
	if strings.Contains(stderr.String(), "未知子命令") {
		t.Fatalf("helper probe routed to agent CLI: %s", stderr.String())
	}
}

func TestRunKeepsAgentCLIForOtherCommands(t *testing.T) {
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := run([]string{"bogus-subcommand"})
	_ = writer.Close()
	os.Stderr = original
	var buf strings.Builder
	_, _ = io.Copy(&buf, reader)
	if code != fleetagent.ExitUsage {
		t.Fatalf("run(bogus-subcommand) = %d, want %d", code, fleetagent.ExitUsage)
	}
	if !strings.Contains(buf.String(), `未知子命令 "bogus-subcommand"`) {
		t.Fatalf("stderr = %q, want unknown-subcommand message", buf.String())
	}
}
