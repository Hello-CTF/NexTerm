package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// The helper store is the one executable every program starts the helper of a state directory from: the desktop,
// the agent and the server each install the helper they carry into <stateDir>/bin, and the store keeps the newest
// release. Whoever spawns the helper then starts the same file, so an older program sharing the directory can no
// longer bring back an older helper.
const (
	helperStoreDir     = "bin"
	helperStoreCurrent = "current.json"
	helperStoreLock    = "helper.store.lock"
	helperStorePrefix  = "nexterm-helper-"
	helperStoreStaging = "staging-"
	// helperStoreLockWait bounds how long an install waits for another program installing at the same time.
	helperStoreLockWait = 10 * time.Second
)

// StoredHelper is the helper a state directory's store starts.
type StoredHelper struct {
	Path    string
	Release RuntimeMetadata
}

type storedHelperRecord struct {
	File    string          `json:"file"`
	Size    int64           `json:"size"`
	Release RuntimeMetadata `json:"release"`
}

// InstallHelper offers source, an executable of this program's release that runs supervisor-helper, to the store
// of stateDir and returns the helper to start. source replaces the stored helper when the store has none (or a
// damaged one), when it is a later release, or when it is a dev build of other content; an older or equal release
// never replaces it. release is source's metadata; its SHA256 is computed when empty.
func InstallHelper(stateDir, source string, release RuntimeMetadata) (StoredHelper, error) {
	if release.SHA256 == "" {
		digest, err := fileSHA256(source)
		if err != nil {
			return StoredHelper{}, fmt.Errorf("hash helper executable: %w", err)
		}
		release.SHA256 = digest
	}
	release.Protocol = ProtocolVersion
	dir := filepath.Join(stateDir, helperStoreDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return StoredHelper{}, fmt.Errorf("create helper store: %w", err)
	}
	unlock, err := lockHelperStore(stateDir)
	if err != nil {
		return StoredHelper{}, err
	}
	defer unlock()
	current, ok := readStoredHelper(dir)
	if ok && !storeReplaces(current.Release, release) {
		return current, nil
	}
	installed, err := writeStoredHelper(dir, source, release)
	if err != nil {
		return StoredHelper{}, err
	}
	keep := []string{filepath.Base(installed.Path)}
	if ok {
		// A replacement that cannot start the new helper starts the previous one again from its file.
		keep = append(keep, filepath.Base(current.Path))
	}
	removeOtherStoredHelpers(dir, keep)
	return installed, nil
}

// CurrentStoredHelper returns the helper the store of stateDir starts, if it holds a valid one.
func CurrentStoredHelper(stateDir string) (StoredHelper, bool) {
	return readStoredHelper(filepath.Join(stateDir, helperStoreDir))
}

// storeReplaces reports whether a program offering offered replaces the stored helper of current.
func storeReplaces(current, offered RuntimeMetadata) bool {
	switch {
	case current.SHA256 == offered.SHA256:
		return false
	case !isReleaseBuild(offered):
		// A dev build is what its developer runs now.
		return true
	case !isReleaseBuild(current):
		// A release never replaces a dev build, so the two do not swap back and forth.
		return false
	default:
		return ReleaseOlder(current, offered)
	}
}

func readStoredHelper(dir string) (StoredHelper, bool) {
	payload, err := os.ReadFile(filepath.Join(dir, helperStoreCurrent))
	if err != nil {
		return StoredHelper{}, false
	}
	var record storedHelperRecord
	if json.Unmarshal(payload, &record) != nil || record.File != filepath.Base(record.File) ||
		!strings.HasPrefix(record.File, helperStorePrefix) || record.Release.SHA256 == "" {
		return StoredHelper{}, false
	}
	path := filepath.Join(dir, record.File)
	// The name carries the digest and the record the size; a truncated or swapped file is not used.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != record.Size || record.File != storedHelperName(record.Release.SHA256) {
		return StoredHelper{}, false
	}
	return StoredHelper{Path: path, Release: record.Release}, true
}

func storedHelperName(digest string) string {
	name := helperStorePrefix + digest[:min(len(digest), 16)]
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func writeStoredHelper(dir, source string, release RuntimeMetadata) (StoredHelper, error) {
	name := storedHelperName(release.SHA256)
	target := filepath.Join(dir, name)
	if fileDigestOrEmpty(target) != release.SHA256 {
		if err := copyHelperExecutable(dir, source, target, release.SHA256); err != nil {
			return StoredHelper{}, err
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return StoredHelper{}, err
	}
	payload, err := json.Marshal(storedHelperRecord{File: name, Size: info.Size(), Release: release})
	if err != nil {
		return StoredHelper{}, err
	}
	if err := writeFileAtomic(dir, helperStoreCurrent, payload, 0o600); err != nil {
		return StoredHelper{}, fmt.Errorf("record stored helper: %w", err)
	}
	return StoredHelper{Path: target, Release: release}, nil
}

// copyHelperExecutable copies source to target through a staging file and checks the copy against digest. A
// running Windows executable cannot be replaced, so the target is never overwritten while it has the right content.
func copyHelperExecutable(dir, source, target, digest string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open helper executable: %w", err)
	}
	defer func() { _ = input.Close() }()
	staging, err := os.CreateTemp(dir, helperStoreStaging+"*")
	if err != nil {
		return fmt.Errorf("stage helper executable: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(staging, hash), input)
	syncErr := staging.Sync()
	closeErr := staging.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		_ = os.Remove(staging.Name())
		return fmt.Errorf("copy helper executable: %w", err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != digest {
		_ = os.Remove(staging.Name())
		return fmt.Errorf("helper executable changed while it was copied (digest %s, want %s)", got, digest)
	}
	if err := os.Chmod(staging.Name(), 0o700); err != nil {
		_ = os.Remove(staging.Name())
		return err
	}
	if err := os.Rename(staging.Name(), target); err != nil {
		_ = os.Remove(staging.Name())
		if fileDigestOrEmpty(target) != digest {
			return fmt.Errorf("install helper executable: %w", err)
		}
	}
	return nil
}

func writeFileAtomic(dir, name string, payload []byte, mode os.FileMode) error {
	staging, err := os.CreateTemp(dir, helperStoreStaging+"*")
	if err != nil {
		return err
	}
	_, writeErr := staging.Write(payload)
	syncErr := staging.Sync()
	closeErr := staging.Close()
	if err := errors.Join(writeErr, syncErr, closeErr, os.Chmod(staging.Name(), mode)); err != nil {
		_ = os.Remove(staging.Name())
		return err
	}
	if err := os.Rename(staging.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(staging.Name())
		return err
	}
	return nil
}

// removeOtherStoredHelpers removes the helpers and staging files other than keep; a file that a running Windows
// helper still executes cannot be removed and stays until a later install.
func removeOtherStoredHelpers(dir string, keep []string) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		name := entry.Name()
		if !slices.Contains(keep, name) && (strings.HasPrefix(name, helperStorePrefix) || strings.HasPrefix(name, helperStoreStaging)) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// lockHelperStore serializes installs into one store across programs, waiting a bounded time for another one.
func lockHelperStore(stateDir string) (func(), error) {
	deadline := time.Now().Add(helperStoreLockWait)
	for {
		unlock, err := lockStateFile(stateDir, helperStoreLock)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, ErrAlreadyExists) || time.Now().After(deadline) {
			return nil, fmt.Errorf("lock helper store: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileDigestOrEmpty(path string) string {
	digest, err := fileSHA256(path)
	if err != nil {
		return ""
	}
	return digest
}

// HelperExecutable returns the helper to start for stateDir: the store's helper after offering it source, or source
// itself when the store cannot be used (the reason is returned as err for the caller to log).
func HelperExecutable(stateDir, source string, release RuntimeMetadata) (string, RuntimeMetadata, error) {
	stored, err := InstallHelper(stateDir, source, release)
	if err != nil {
		if release.SHA256 == "" {
			release.SHA256 = fileDigestOrEmpty(source)
		}
		release.Protocol = ProtocolVersion
		return source, release, err
	}
	return stored.Path, stored.Release, nil
}
