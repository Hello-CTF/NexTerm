package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type waitStream struct {
	fakeCommandStream
	code int
	err  error
}

func (s *waitStream) Wait(context.Context) (int, error) { return s.code, s.err }

func readAllChunked(t *testing.T, reader io.Reader, size int) (string, error) {
	t.Helper()
	var out strings.Builder
	buffer := make([]byte, size)
	for range 10000 {
		count, err := reader.Read(buffer)
		out.Write(buffer[:count])
		if err != nil {
			return out.String(), err
		}
	}
	t.Fatal("reader did not terminate")
	return "", nil
}

func TestComposeResultReaderAppendsExitTrailer(t *testing.T) {
	for _, size := range []int{1, 7, 4096} {
		t.Run("exit zero", func(t *testing.T) {
			result := &composeExecutionResult{}
			reader := &composeResultReader{reader: io.NopCloser(strings.NewReader("pulled\n")), stream: &waitStream{}, ctx: t.Context(), result: result}
			got, err := readAllChunked(t, reader, size)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("err = %v, want EOF", err)
			}
			if !strings.HasPrefix(got, "pulled\n") || !strings.Contains(got, "[NexTerm]") || !strings.Contains(got, "退出码 0") {
				t.Fatalf("output = %q", got)
			}
			if !result.completed || result.exitCode != 0 {
				t.Fatalf("result not recorded: %+v", result)
			}
		})
		t.Run("exit two", func(t *testing.T) {
			result := &composeExecutionResult{}
			reader := &composeResultReader{reader: io.NopCloser(strings.NewReader("boom")), stream: &waitStream{code: 2}, ctx: t.Context(), result: result}
			got, err := readAllChunked(t, reader, size)
			var exit *composeExitError
			if !errors.As(err, &exit) || exit.code != 2 {
				t.Fatalf("err = %v, want exit error 2", err)
			}
			if !strings.HasPrefix(got, "boom") || !strings.Contains(got, "退出码 2") {
				t.Fatalf("output = %q", got)
			}
		})
		t.Run("wait failure", func(t *testing.T) {
			result := &composeExecutionResult{}
			waitErr := errors.New("channel lost")
			reader := &composeResultReader{reader: io.NopCloser(strings.NewReader("")), stream: &waitStream{err: waitErr}, ctx: t.Context(), result: result}
			got, err := readAllChunked(t, reader, size)
			if !errors.Is(err, waitErr) {
				t.Fatalf("err = %v, want wait error", err)
			}
			if !strings.Contains(got, "结束状态未知") || !strings.Contains(got, "channel lost") {
				t.Fatalf("output = %q", got)
			}
		})
	}
}

func TestComposeStacksUpCommandMatchesExecutedCommand(t *testing.T) {
	project := "shop';printf injected;#"
	workingDir := "/srv/shop"
	file := workingDir + "/compose.yml"
	cli := &fakeDockerCLI{handler: func(command string) CommandResult {
		switch command {
		case "docker '--context' 'remote' 'ps' '-a' '--format' '{{json .}}'":
			return CommandResult{Stdout: composePS("c1", "web", project, "web")}
		case "docker '--context' 'remote' 'inspect' 'c1'":
			return CommandResult{Stdout: composeInspect(project, workingDir, file)}
		}
		return CommandResult{}
	}}
	opener := &recordingOpener{stream: &fakeCommandStream{}}
	backend := NewCommandBackend(cli, opener, ShellPOSIX, WithDockerContext("remote"))
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: backend})
	overview, err := service.ComposeStacks(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Stacks) != 1 || overview.Stacks[0].ReadOnly {
		t.Fatalf("unexpected stacks: %+v", overview.Stacks)
	}
	stack := overview.Stacks[0]
	if stack.UpCommand == "" || !strings.Contains(stack.UpCommand, "'--context' 'remote'") {
		t.Fatalf("upCommand = %q", stack.UpCommand)
	}
	stream, err := backend.OpenCompose(t.Context(), overview.CLI, stack, ComposeActionUp, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Reader.Close()
	if len(opener.commands) != 1 || opener.commands[0] != stack.UpCommand {
		t.Fatalf("executed %q, upCommand %q", opener.commands, stack.UpCommand)
	}
}

func TestComposeStacksReadOnlyHasNoUpCommand(t *testing.T) {
	cli := &fakeDockerCLI{handler: func(command string) CommandResult {
		switch command {
		case "docker 'ps' '-a' '--format' '{{json .}}'":
			return CommandResult{Stdout: composePS("c1", "web", "shop", "web")}
		case "docker 'inspect' 'c1'":
			return CommandResult{Stdout: composeInspect("shop", "/srv/shop", "compose.yml")}
		case "docker 'compose' 'version'", "docker-compose 'version'":
			return commandFailure(127, "unknown command")
		}
		return CommandResult{}
	}}
	service := NewService(&stubProvider{sdkErr: ErrNoBackend, command: NewCommandBackend(cli, nil, ShellPOSIX)})
	overview, err := service.ComposeStacks(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, stack := range overview.Stacks {
		if !stack.ReadOnly || stack.UpCommand != "" {
			t.Fatalf("read-only stack carries upCommand: %+v", stack)
		}
	}
}
