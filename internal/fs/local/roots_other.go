//go:build !windows

package local

import (
	"runtime"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func platformRoots() (base.FileSystemRoots, error) {
	return base.FileSystemRoots{
		PathStyle:    base.PathStylePOSIX,
		Roots:        []base.FileRoot{{Path: "/", Label: "/"}},
		Supported:    true,
		Capabilities: Capabilities(runtime.GOOS),
	}, nil
}

func normalizeFilesystemError(err error) error {
	return err
}
