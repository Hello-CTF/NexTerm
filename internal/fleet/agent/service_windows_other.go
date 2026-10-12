//go:build !windows

package agent

func newWindowsManager(CommandRunner) ServiceManager {
	return unsupportedManager{}
}
