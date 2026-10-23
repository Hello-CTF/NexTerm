package docker

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyCommandFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		code    int
		message string
		want    error
	}{
		{"bash missing binary", 127, "bash: line 1: docker: command not found", ErrNotInstalled},
		{"dash missing binary", 127, "sh: 1: docker: not found", ErrNotInstalled},
		{"powershell missing binary", 1, "docker : The term 'docker' is not recognized as the name of a cmdlet", ErrNotInstalled},
		{"daemon down", 1, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", ErrDaemonUnavailable},
		{"socket permission", 1, "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock", ErrAccessDenied},
		// A command missing inside a container must not read as "no Docker".
		{"missing command in container", 127, `OCI runtime exec failed: exec failed: unable to start container process: exec: "sh": executable file not found in $PATH`, nil},
		{"ordinary failure", 1, "Error response from daemon: No such container: web", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyCommandFailure(test.code, test.message); got != test.want {
				t.Fatalf("classifyCommandFailure = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCommandBackendReportsMissingDocker(t *testing.T) {
	code := 127
	backend := NewCommandBackend(CommandRunnerFunc(func(context.Context, string) (CommandResult, error) {
		return CommandResult{Stderr: "bash: line 1: docker: command not found\n", ExitCode: &code}, nil
	}), nil, ShellPOSIX)
	_, err := backend.ListContainerDTOs(t.Context(), true)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
	// The SDK attempt fails first in production; the joined error must still classify.
	service := NewService(&stubProvider{sdkErr: errors.New("dial unix /var/run/docker.sock: connect: no such file or directory"), command: backend})
	if _, err := service.PS(t.Context(), "s1"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("service err = %v, want ErrNotInstalled", err)
	}
}
