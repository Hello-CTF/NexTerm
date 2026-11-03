package supervisor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// HelperInstanceIDFile is the file in a helper state directory that holds its random instance id.
const HelperInstanceIDFile = "helper-instance-id"

// HelperInstanceID returns the random id persisted in stateDir, creating it on first use. Unlike the
// state digest, which only depends on uid and path, it tells apart two machines that share both.
// Concurrent first calls agree on one id through link. A malformed or unsafe file of this user is
// replaced by rename, which swaps the path itself and never writes through a symlink; concurrent
// repairs may briefly return different ids, the last rename wins and later calls agree.
func HelperInstanceID(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("supervisor helper state directory: %w", err)
	}
	final := filepath.Join(dir, HelperInstanceIDFile)
	id, err := readInstanceID(final)
	if err == nil {
		return id, nil
	}
	missing := errors.Is(err, os.ErrNotExist)
	if !missing && !errors.Is(err, errInstanceIDInvalid) {
		return "", err
	}
	temp, err := writeTempInstanceID(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(temp) }()
	if missing {
		if err := os.Link(temp, final); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("publish helper instance id: %w", err)
		}
	} else if err := os.Rename(temp, final); err != nil {
		return "", fmt.Errorf("replace helper instance id: %w", err)
	}
	// The re-read, not the temp id, is the answer: another caller may have won.
	return readInstanceID(final)
}

var errInstanceIDInvalid = errors.New("helper instance id is malformed")

func readInstanceID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	// A file of another user is never replaced; check the owner first.
	if err := instanceIDOwner(info); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrInvalidInput, path, err)
	}
	// A symlink, a non-regular file or a group/world accessible file is ours
	// but unusable: report it invalid so rename replaces the path itself.
	if err := privateRegularFile(info); err != nil {
		return "", errInstanceIDInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(string(data), "\n")
	if len(id) != 32 {
		return "", errInstanceIDInvalid
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errInstanceIDInvalid
		}
	}
	return id, nil
}

func writeTempInstanceID(dir string) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".helper-instance-id-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	_, err = file.WriteString(hex.EncodeToString(raw) + "\n")
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
