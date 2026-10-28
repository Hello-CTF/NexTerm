//go:build !darwin && !ios && !android && cgo

package inference

func CheckPlatform() error { return nil }
