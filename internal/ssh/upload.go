package ssh

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// UploadSource describes a local supervisor-capable binary staged to targets
// that have none.
type UploadSource struct {
	Path   string
	GOOS   string
	GOARCH string
	Digest string
	CGO    bool
}

// InspectExecutable reads the build info and content digest of a local
// binary, so uploads can be gated on target compatibility.
func InspectExecutable(path string) (UploadSource, error) {
	info, err := os.Stat(path)
	if err != nil {
		return UploadSource{}, err
	}
	if !info.Mode().IsRegular() {
		return UploadSource{}, fmt.Errorf("%s is not a regular file", path)
	}
	build, err := buildinfo.ReadFile(path)
	if err != nil {
		return UploadSource{}, fmt.Errorf("read build info of %s: %w", path, err)
	}
	source := UploadSource{Path: path}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "GOOS":
			source.GOOS = setting.Value
		case "GOARCH":
			source.GOARCH = setting.Value
		case "CGO_ENABLED":
			source.CGO = setting.Value == "1"
		}
	}
	if source.GOOS == "" || source.GOARCH == "" {
		return UploadSource{}, fmt.Errorf("%s carries no GOOS/GOARCH build info", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return UploadSource{}, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return UploadSource{}, fmt.Errorf("digest %s: %w", path, err)
	}
	source.Digest = hex.EncodeToString(digest.Sum(nil))
	return source, nil
}
