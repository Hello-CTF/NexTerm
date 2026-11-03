//go:build !windows

package agent

// currentProcessInJob is always false: job objects exist only on Windows.
func currentProcessInJob() (bool, error) { return false, nil }
