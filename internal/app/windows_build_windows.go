//go:build windows

package app

import "golang.org/x/sys/windows"

func hostWindowsBuild() uint32 {
	return windows.RtlGetVersion().BuildNumber
}
