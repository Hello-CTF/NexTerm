//go:build !windows

package agent

func newWindowsManager(CommandRunner, ...string) ServiceManager {
	return unsupportedManager{}
}
