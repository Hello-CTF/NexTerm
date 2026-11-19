package localhelper

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// bundledHelper returns the standalone helper for this platform that a package ships next to executable
// (nexterm-helper-<goos>-<arch>, the file SSH hosts receive), if there is one. A build run from its source tree has
// none and starts the helper from its own executable.
func bundledHelper(executable string) (string, bool) {
	architectures := []string{runtime.GOARCH}
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm" {
		architectures = []string{"armv7", "arm"}
	}
	for _, architecture := range architectures {
		name := "nexterm-helper-" + runtime.GOOS + "-" + architecture
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(filepath.Dir(executable), name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, true
		}
	}
	return "", false
}

var (
	bundledMu       sync.Mutex
	bundledReleases = map[string]bundledEntry{}
)

type bundledEntry struct {
	size    int64
	modTime int64
	release supervisor.RuntimeMetadata
}

// bundledRelease is the metadata of a packaged helper, a build of this release; its digest is computed once per
// file content seen.
func bundledRelease(path string) (supervisor.RuntimeMetadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		return supervisor.RuntimeMetadata{}, err
	}
	bundledMu.Lock()
	defer bundledMu.Unlock()
	if entry, ok := bundledReleases[path]; ok && entry.size == info.Size() && entry.modTime == info.ModTime().UnixNano() {
		return entry.release, nil
	}
	release, err := supervisor.RuntimeMetadataForExecutable(path)
	if err != nil {
		return supervisor.RuntimeMetadata{}, err
	}
	bundledReleases[path] = bundledEntry{size: info.Size(), modTime: info.ModTime().UnixNano(), release: release}
	return release, nil
}
