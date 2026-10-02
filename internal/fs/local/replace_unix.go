//go:build unix

package local

import "os"

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}
