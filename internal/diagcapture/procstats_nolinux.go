//go:build !linux

package diagcapture

func currentRSSBytes() int64 { return 0 }

func openFDCount() int { return 0 }
