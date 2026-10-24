//go:build !linux && !darwin

package diagcapture

func readProcessUsage() processUsage { return processUsage{} }
