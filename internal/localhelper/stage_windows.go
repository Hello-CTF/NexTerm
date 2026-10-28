//go:build windows

package localhelper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// stageExecutable returns a copy of the executable named by its digest. A
// running Windows executable cannot be replaced, so a helper started from the
// installed binary would make the next app update fail; the copy keeps the
// installed file free. Copies of other builds are removed when no helper runs
// them anymore.
func stageExecutable(stateDir, executable string) (string, error) {
	source, err := os.Open(executable)
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, source); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	dir := filepath.Join(stateDir, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "nexterm-helper-" + digest[:16] + ".exe"
	target := filepath.Join(dir, name)
	// An existing copy is used only when its content still has the digest
	// in its name; anything else is replaced.
	if fileDigest(target) != digest {
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		staging, err := os.CreateTemp(dir, "staging-*.exe")
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(staging, source)
		closeErr := staging.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(staging.Name())
			return "", fmt.Errorf("copy helper executable: %v %v", copyErr, closeErr)
		}
		if err := os.Rename(staging.Name(), target); err != nil {
			_ = os.Remove(staging.Name())
			if fileDigest(target) != digest {
				return "", err
			}
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Name() != name && (strings.HasPrefix(entry.Name(), "nexterm-helper-") || strings.HasPrefix(entry.Name(), "staging-")) {
			// A copy that a running helper still uses cannot be removed.
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return target, nil
}

// fileDigest returns the hex SHA-256 of path, or "" when it cannot be read.
func fileDigest(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}
