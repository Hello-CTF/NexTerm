//go:build !darwin

package agent

func launchdUID() string {
	return ""
}
