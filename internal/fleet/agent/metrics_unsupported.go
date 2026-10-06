//go:build !linux

package agent

import "time"

type unsupportedCollector struct{}

func newPlatformCollector(string, func() time.Time) Collector {
	return unsupportedCollector{}
}

func (unsupportedCollector) Collect() (Sample, error) {
	return Sample{}, ErrMetricsUnsupported
}
