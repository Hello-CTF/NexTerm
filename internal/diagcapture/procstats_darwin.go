//go:build darwin

package diagcapture

import "os"

// Darwin has no cheap current-RSS source without cgo; maxRssBytes from getrusage covers the peak.
func currentRSSBytes() int64 { return 0 }

func openFDCount() int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0
	}
	return len(entries)
}
