//go:build windows

package local

import "os"

func ownerGroup(info os.FileInfo) (string, string) {
	return "", ""
}
