//go:build !ios && !android

package biometric

// Placeholder implementation for non-iOS/Android platforms: capability unavailable, every call returns ErrUnavailable,
// keeping repo-wide go build / go vet / go test green on mac, linux, and windows.

func Current() (Status, error) {
	return Status{Available: false}, nil
}

func Unlock() (string, error) {
	return "", ErrUnavailable
}

func Enable(string) error {
	return ErrUnavailable
}

func Disable() error {
	return ErrUnavailable
}
