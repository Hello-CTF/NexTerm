package production

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestTerminalIPCErrorExplainsDockerHostProblems(t *testing.T) {
	sdkFailure := errors.New("dial unix /var/run/docker.sock: connect: no such file or directory")
	for _, test := range []struct {
		cause error
		code  ipc.Code
	}{
		{docker.ErrNotInstalled, "docker_not_installed"},
		{docker.ErrDaemonUnavailable, "docker_unavailable"},
		{docker.ErrAccessDenied, "docker_forbidden"},
	} {
		// Production joins the SDK attempt with the command-line fallback failure.
		err := errors.Join(sdkFailure, fmt.Errorf("%w: docker command exited with code 127", test.cause))
		var mapped *ipc.Error
		if !errors.As(terminalIPCError(err), &mapped) || mapped.Code != test.code || mapped.Message == "" {
			t.Fatalf("%v mapped to %+v, want code %s with a message", test.cause, mapped, test.code)
		}
	}
}
