//go:build race

package terminal

// raceDetectorEnabled reports whether the test binary was built with
// -race. Throughput assertions are skipped then: race instrumentation
// slows the VT path down by more than an order of magnitude and measures
// the detector, not the implementation.
const raceDetectorEnabled = true
