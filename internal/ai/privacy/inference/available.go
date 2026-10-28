package inference

import (
	"os"
	"path/filepath"
)

// Available reports whether this device can run the local engine without
// loading it: the platform must qualify and the ONNX Runtime library for this
// OS must be present in directory.
func Available(directory string) error {
	if err := CheckPlatform(); err != nil {
		return err
	}
	if directory == "" {
		return ErrUnavailable
	}
	_, onnx := RuntimeFiles()
	info, err := os.Stat(filepath.Join(directory, onnx))
	if err != nil || !info.Mode().IsRegular() {
		return ErrUnavailable
	}
	return nil
}
