// Package transfer holds the transfer options and archive naming shared by the file systems. It imports no
// transport, so the host helper's file service can use it without linking the SSH and SFTP clients.
package transfer

import (
	"strings"
	"time"
)

type Progress struct {
	Transferred int64
	Total       int64
	Done        bool
}

type Options struct {
	Resume           bool
	ProgressInterval time.Duration
	BufferSize       int
	Progress         func(Progress)
	// MaxBytesPerSecond caps the upload rate so a background transfer cannot starve
	// interactive traffic sharing the same SSH connection. Zero means unlimited.
	MaxBytesPerSecond int64
	// MaxBytes caps the size of a downloaded file; zero means unlimited. A source larger than the cap is
	// refused before anything is written.
	MaxBytes int64
}

func StripArchiveExtension(name string) string {
	lower := strings.ToLower(name)
	for _, extension := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tgz", ".tbz2", ".tbz", ".txz", ".tar", ".zip"} {
		if strings.HasSuffix(lower, extension) {
			return name[:len(name)-len(extension)]
		}
	}
	return name
}
