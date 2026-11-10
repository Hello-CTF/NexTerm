//go:build !windows

package local

import (
	"io/fs"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func platformFileKind(_ string, info fs.FileInfo) base.FileKind {
	return fileKind(info.Mode())
}

func platformHidden(name string, _ fs.FileInfo) bool {
	return base.DotHidden(name)
}
