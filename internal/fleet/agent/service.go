package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ServiceManager installs and inspects the per-user autostart unit for the
// agent. Implementations must report honestly when the service manager is
// unavailable (for example no user systemd bus): Installed reflects the unit
// file on disk while Enabled/Active stay false and LastError explains why.
type ServiceManager interface {
	Install(ctx context.Context, executable, dataDir string) error
	Uninstall(ctx context.Context) error
	Status(ctx context.Context) ServiceState
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return string(output), err
		}
		return string(output), fmt.Errorf("%s unavailable: %w", name, err)
	}
	return string(output), nil
}

func NewServiceManager(goos string, runner CommandRunner, getenv func(string) string, homeDir string) ServiceManager {
	if runner == nil {
		runner = execRunner{}
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	switch goos {
	case "linux":
		return newSystemdManager(runner, getenv, homeDir)
	case "darwin":
		return newLaunchdManager(runner, homeDir, launchdUID())
	default:
		return unsupportedManager{}
	}
}

type unsupportedManager struct{}

func (unsupportedManager) Install(context.Context, string, string) error {
	return ErrServiceUnsupported
}

func (unsupportedManager) Uninstall(context.Context) error {
	return ErrServiceUnsupported
}

func (unsupportedManager) Status(context.Context) ServiceState {
	return ServiceState{LastError: ErrServiceUnsupported.Error()}
}

func trimOutput(output string) string {
	return strings.TrimSpace(string(output))
}
