package inference

// Available reports whether this platform can run local privacy inference
// with the ONNX Runtime library in directory. It does not load the library.
func Available(directory string) error {
	if err := CheckPlatform(); err != nil {
		return err
	}
	_, err := runtimePath(directory)
	return err
}
