//go:build !darwin

package terminal

func nativeProcessCPUTime() (float64, bool) {
	return 0, false
}
