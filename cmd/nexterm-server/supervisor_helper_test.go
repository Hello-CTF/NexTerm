//go:build darwin || linux

package main

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestServerBinaryServesSupervisorHelperCommand(t *testing.T) {
	binary := buildServerBinary(t)
	cmd := exec.Command(binary, supervisor.HelperCommand, "--probe")
	cmd.Env = serverProcessEnv(t)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("supervisor-helper --probe: %v", err)
	}
	if string(output) != strconv.Itoa(supervisor.ProtocolVersion)+"\n" {
		t.Fatalf("probe output = %q, want the protocol version", output)
	}

	cmd = exec.Command(binary, supervisor.HelperCommand, "--bogus")
	cmd.Env = serverProcessEnv(t)
	err = cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("supervisor-helper --bogus error = %v, want exit code 2", err)
	}
}

func TestSupervisorHelperProbePrintsProtocolVersion(t *testing.T) {
	code, output := runSupervisorHelperCLI(t, []string{"--probe"})
	if code != 0 {
		t.Fatalf("probe exit code = %d, output %q", code, output)
	}
	if output != strconv.Itoa(supervisor.ProtocolVersion)+"\n" {
		t.Fatalf("probe output = %q, want the protocol version", output)
	}
}

func TestSupervisorHelperRequiresStateOrDataDirectory(t *testing.T) {
	t.Setenv("NEXTERM_DATA_DIR", "")
	code, _ := runSupervisorHelperCLI(t, nil)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 without a state or data directory", code)
	}
}

func TestSupervisorHelperRejectsUnknownOptions(t *testing.T) {
	code, _ := runSupervisorHelperCLI(t, []string{"--bogus"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for an unknown option", code)
	}
	code, _ = runSupervisorHelperCLI(t, []string{"--state-dir"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for a missing option value", code)
	}
	code, _ = runSupervisorHelperCLI(t, []string{"positional"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for an unexpected positional argument", code)
	}
}

func runSupervisorHelperCLI(t *testing.T, args []string) (int, string) {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := supervisor.RunHelperCLI(args)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = original
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if rest := strings.TrimSpace(string(output)); code == 0 && rest == "" {
		t.Fatal("helper command produced no output")
	}
	return code, string(output)
}
