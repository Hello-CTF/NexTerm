package ssh

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var (
	ErrDigestMismatch = errors.New("helper content SHA256 mismatch")
	ErrTrust          = errors.New("helper trust validation failed")
)

// UploadSource describes a local supervisor-capable binary staged to targets
// that have none. Version and Commit are evidence, not proof of running identity.
type UploadSource struct {
	Path         string
	GOOS         string
	GOARCH       string
	GOARM        string
	Digest       string
	CGO          bool
	Version      string
	Commit       string
	Dirty        bool
	ManifestPath string
}

func (s UploadSource) ValidateForTarget(target Target) error {
	if err := target.validatePlatform(); err != nil {
		return err
	}
	if s.GOOS != target.GOOS || s.GOARCH != target.GOARCH || s.GOARM != target.GOARM {
		return fmt.Errorf("%w: local binary targets %s/%s GOARM=%s but target is %s", base.ErrUnsupported, s.GOOS, s.GOARCH, s.GOARM, target.ID())
	}
	if s.CGO {
		return fmt.Errorf("%w: local binary links CGO and may not run on the target", base.ErrUnsupported)
	}
	if err := validateContentDigest(s.Digest); err != nil {
		return fmt.Errorf("%w: %v", ErrTrust, err)
	}
	return nil
}

// InspectExecutable reads build metadata and hashes the actual file content.
func InspectExecutable(path string) (UploadSource, error) {
	return InspectExecutableContext(context.Background(), path)
}

// InspectExecutableContext is the cancellable form of InspectExecutable.
func InspectExecutableContext(ctx context.Context, path string) (UploadSource, error) {
	if err := ctx.Err(); err != nil {
		return UploadSource{}, err
	}
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
	if build.Main.Version != "" && build.Main.Version != "(devel)" {
		source.Version = build.Main.Version
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "GOOS":
			source.GOOS = setting.Value
		case "GOARCH":
			source.GOARCH = setting.Value
		case "GOARM":
			source.GOARM = setting.Value
		case "CGO_ENABLED":
			source.CGO = setting.Value == "1"
		case "vcs.revision":
			source.Commit = setting.Value
		case "vcs.modified":
			source.Dirty = setting.Value == "true"
		}
	}
	if source.GOOS == "" || source.GOARCH == "" {
		return UploadSource{}, fmt.Errorf("%s carries no GOOS/GOARCH build info", path)
	}
	if source.GOARCH == "arm" && source.GOARM == "" {
		return UploadSource{}, fmt.Errorf("%s carries GOARCH=arm without required GOARM build info", path)
	}
	digest, err := digestFileContext(ctx, path)
	if err != nil {
		return UploadSource{}, err
	}
	source.Digest = digest
	if err := ctx.Err(); err != nil {
		return UploadSource{}, err
	}
	if err := applyExecutableManifest(&source, info.Size()); err != nil {
		return UploadSource{}, err
	}
	return source, nil
}

func digestFileContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	buffer := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := digest.Write(buffer[:count]); err != nil {
				return "", fmt.Errorf("digest %s: %w", path, err)
			}
		}
		if readErr == io.EOF {
			return hex.EncodeToString(digest.Sum(nil)), nil
		}
		if readErr != nil {
			return "", fmt.Errorf("digest %s: %w", path, readErr)
		}
	}
}

type executableManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	GOARM         string `json:"goarm"`
	SHA256        string `json:"sha256"`
	Platform      struct {
		OS    string `json:"os"`
		Arch  string `json:"arch"`
		CGO   string `json:"cgo"`
		GOARM string `json:"goarm"`
	} `json:"platform"`
	Artifact struct {
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	} `json:"artifact"`
}

func applyExecutableManifest(source *UploadSource, size int64) error {
	manifestPath := source.Path + ".manifest.json"
	raw, err := os.ReadFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read executable manifest: %v", ErrTrust, err)
	}
	var manifest executableManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("%w: decode executable manifest %s: %v", ErrTrust, manifestPath, err)
	}
	if manifest.SchemaVersion != 1 || manifest.Kind != "go-binary" {
		return fmt.Errorf("%w: executable manifest %s has unsupported schema or kind", ErrTrust, manifestPath)
	}
	cgo := "0"
	if source.CGO {
		cgo = "1"
	}
	manifestArchitecture := source.GOARCH
	if source.GOARCH == "arm" && source.GOARM == "7" {
		manifestArchitecture = "armv7"
	}
	if manifest.Platform.OS != source.GOOS || manifest.Platform.Arch != manifestArchitecture || manifest.Platform.CGO != cgo {
		return fmt.Errorf("%w: executable manifest platform does not match Go build metadata", ErrTrust)
	}
	manifestGOARM := manifest.Platform.GOARM
	if manifest.GOARM != "" {
		if manifestGOARM != "" && manifestGOARM != manifest.GOARM {
			return fmt.Errorf("%w: executable manifest contains conflicting GOARM values", ErrTrust)
		}
		manifestGOARM = manifest.GOARM
	}
	if source.GOARCH == "arm" && manifestGOARM != source.GOARM {
		return fmt.Errorf("%w: executable manifest GOARM %q does not match build metadata %q", ErrTrust, manifestGOARM, source.GOARM)
	}
	if source.GOARCH != "arm" && manifestGOARM != "" {
		return fmt.Errorf("%w: executable manifest unexpectedly declares GOARM", ErrTrust)
	}
	if manifest.Artifact.SizeBytes != size {
		return fmt.Errorf("%w: executable manifest size %d does not match content size %d", ErrTrust, manifest.Artifact.SizeBytes, size)
	}
	if !strings.EqualFold(manifest.Artifact.SHA256, source.Digest) {
		return fmt.Errorf("%w: executable manifest artifact digest does not match content: %w", ErrTrust, ErrDigestMismatch)
	}
	if manifest.SHA256 != "" && !strings.EqualFold(manifest.SHA256, source.Digest) {
		return fmt.Errorf("%w: executable manifest runtime digest does not match content: %w", ErrTrust, ErrDigestMismatch)
	}
	manifestCommit := strings.TrimSpace(manifest.Commit)
	if manifestCommit == "unknown" {
		manifestCommit = ""
	}
	if source.Commit != "" && manifestCommit != "" && source.Commit != manifestCommit {
		return fmt.Errorf("%w: executable manifest commit %q conflicts with Go build metadata %q", ErrTrust, manifestCommit, source.Commit)
	}
	if manifestCommit != "" {
		source.Commit = manifestCommit
	}
	if version := strings.TrimSpace(manifest.Version); version != "" {
		source.Version = version
	}
	source.ManifestPath = manifestPath
	return nil
}
