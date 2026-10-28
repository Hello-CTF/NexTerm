//go:build (linux || windows) && !android && (amd64 || arm64)

package inference

func selfLibraryPath() string { return "" }

func CheckPlatform() error { return nil }
