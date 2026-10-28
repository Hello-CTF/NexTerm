//go:build !cgo

package inference

func CheckPlatform() error { return ErrUnavailable }
