package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type recordingRunner struct {
	commands []string
	result   CommandResult
	err      error
}

func (r *recordingRunner) RunCommand(_ context.Context, command string) (CommandResult, error) {
	r.commands = append(r.commands, command)
	return r.result, r.err
}

type recordingOpener struct {
	commands []string
	stream   CommandStream
	err      error
}

func (o *recordingOpener) OpenCommand(_ context.Context, command string, _, _ uint) (CommandStream, error) {
	o.commands = append(o.commands, command)
	return o.stream, o.err
}

func TestCommandBackendListDoesNotTruncate(t *testing.T) {
	var output strings.Builder
	for index := range 569 {
		fmt.Fprintf(&output, "{\"ID\":\"%012d\",\"Names\":\"web-%d\",\"Image\":\"nginx\",\"State\":\"running\",\"Status\":\"Up\",\"Ports\":\"80/tcp\",\"Labels\":\"com.docker.compose.project=shop\"}\n", index, index)
	}
	runner := &recordingRunner{result: CommandResult{Stdout: output.String()}}
	backend := NewCommandBackend(runner, nil, ShellPOSIX)
	containers, err := backend.ListContainerDTOs(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 569 || containers[568].Name != "web-568" {
		t.Fatalf("unexpected container count or last row: %d", len(containers))
	}
	if len(runner.commands) != 1 || runner.commands[0] != "docker 'ps' '-a' '--format' '{{json .}}'" {
		t.Fatalf("unexpected ps command: %q", runner.commands)
	}
}

func TestCommandBackendQuotesArgumentsAndPreservesCustomCommand(t *testing.T) {
	runner := &recordingRunner{}
	opener := &recordingOpener{stream: &fakeCommandStream{}}
	backend := NewCommandBackend(runner, opener, ShellPOSIX)
	if err := backend.Action(t.Context(), ActionOptions{Container: "web; printf injected", Action: ActionRestart}); err != nil {
		t.Fatal(err)
	}
	want := "docker 'restart' 'web; printf injected'"
	if runner.commands[0] != want {
		t.Fatalf("command = %q, want %q", runner.commands[0], want)
	}
	custom := "printf '%s\\n' \"custom  $HOME\"; exec sh"
	if _, err := backend.OpenRawExec(t.Context(), custom, 80, 24); err != nil {
		t.Fatal(err)
	}
	if opener.commands[0] != custom {
		t.Fatalf("custom command changed: %q", opener.commands[0])
	}
}

func TestCommandBackendPowerShellQuoting(t *testing.T) {
	runner := &recordingRunner{}
	backend := NewCommandBackend(runner, nil, ShellPowerShell)
	_, err := backend.OpenLogs(t.Context(), LogsOptions{Container: "a'b", Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if runner.commands[0] != "docker 'logs' '--tail' '10' 'a''b'" {
		t.Fatalf("unexpected PowerShell command: %q", runner.commands[0])
	}
}

func TestCommandBackendReportsFailureAndTruncation(t *testing.T) {
	code := 2
	for _, test := range []struct {
		name   string
		result CommandResult
	}{
		{name: "exit", result: CommandResult{ExitCode: &code, Stderr: "bad daemon"}},
		{name: "truncated", result: CommandResult{Truncated: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := NewCommandBackend(&recordingRunner{result: test.result}, nil, ShellPOSIX)
			if _, err := backend.ListImageDTOs(t.Context()); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestCommandBackendLogsIncludeStderr(t *testing.T) {
	runner := &recordingRunner{result: CommandResult{Stdout: "stdout\n", Stderr: "stderr\n"}}
	backend := NewCommandBackend(runner, nil, ShellPOSIX)
	stream, err := backend.OpenLogs(t.Context(), LogsOptions{Container: "c1", Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	output, err := readDemultiplexed(stream.Reader, stream.TTY)
	if err != nil || output != "stdout\nstderr\n" {
		t.Fatalf("logs = %q, %v", output, err)
	}
}

func TestRunnerOnlyWinRMExecReturnsOutputAndNonZeroExit(t *testing.T) {
	exitCode := 7
	runner := &recordingRunner{result: CommandResult{Stdout: "stdout\n", Stderr: "stderr\n", ExitCode: &exitCode}}
	command := NewCommandBackend(runner, nil, ShellPowerShell)
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: command})
	result, err := service.Exec(t.Context(), ExecRequest{SessionID: "s1", Container: "c1", Command: "printf test"})
	if err != nil || result.Output != "stdout\nstderr\n" || result.ExitCode != 7 {
		t.Fatalf("exec = %+v, %v", result, err)
	}
	want := "docker 'exec' 'c1' 'sh' '-c' 'printf test'"
	if len(runner.commands) != 1 || runner.commands[0] != want {
		t.Fatalf("commands = %q, want %q", runner.commands, want)
	}
}

func TestCommandExecFallbackReadsRawNonTTYOutput(t *testing.T) {
	opener := &recordingOpener{stream: &bufferCommandStream{Reader: strings.NewReader("plain output")}}
	command := NewCommandBackend(nil, opener, ShellPOSIX)
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: command})
	result, err := service.Exec(t.Context(), ExecRequest{SessionID: "s1", Container: "c1", Command: "printf test"})
	if err != nil || result.Output != "plain output" {
		t.Fatalf("exec = %+v, %v", result, err)
	}
}

type fakeCommandStream struct{}

type bufferCommandStream struct {
	*strings.Reader
}

func (s *bufferCommandStream) NextOutput(context.Context) (base.OutputEvent, error) {
	if s.Reader == nil {
		return base.OutputEvent{}, io.EOF
	}
	data, _ := io.ReadAll(s.Reader)
	s.Reader = nil
	if len(data) == 0 {
		return base.OutputEvent{}, io.EOF
	}
	return base.OutputEvent{Data: data}, nil
}
func (s *bufferCommandStream) Write(p []byte) (int, error)              { return len(p), nil }
func (s *bufferCommandStream) Close() error                             { return nil }
func (s *bufferCommandStream) CloseWrite() error                        { return nil }
func (s *bufferCommandStream) Resize(context.Context, uint, uint) error { return nil }
func (s *bufferCommandStream) Wait(context.Context) (int, error)        { return 0, nil }

func (s *fakeCommandStream) Read([]byte) (int, error) { return 0, nil }
func (s *fakeCommandStream) NextOutput(context.Context) (base.OutputEvent, error) {
	return base.OutputEvent{}, io.EOF
}
func (s *fakeCommandStream) Write(p []byte) (int, error)              { return len(p), nil }
func (s *fakeCommandStream) Close() error                             { return nil }
func (s *fakeCommandStream) CloseWrite() error                        { return nil }
func (s *fakeCommandStream) Resize(context.Context, uint, uint) error { return nil }
func (s *fakeCommandStream) Wait(context.Context) (int, error)        { return 0, nil }
