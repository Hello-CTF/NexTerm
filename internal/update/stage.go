package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A verified package waits in the stage directory until the restart that applies it:
//
//	<stage>/staged.json            the manifest, written last
//	<stage>/package/<asset name>   the package
//
// A package that the app exits without applying stays staged and the next launch offers its restart again, unless
// that launch already runs the staged version or a newer one, in which case it is discarded.
const (
	stagedManifestName = "staged.json"
	stagedPackageDir   = "package"
)

type stagedPackage struct {
	Version   string `json:"version"`
	AssetName string `json:"assetName"`
	SHA256    string `json:"sha256"`
}

func (p stagedPackage) path(directory string) string {
	return filepath.Join(directory, stagedPackageDir, p.AssetName)
}

func (p stagedPackage) valid() bool {
	return p.Version != "" && p.AssetName != "" && filepath.Base(p.AssetName) == p.AssetName &&
		p.AssetName != "." && p.AssetName != ".." && len(p.SHA256) == 64
}

// verify recomputes the package's SHA-256, so a package changed after it was verified is never applied.
func (p stagedPackage) verify(directory string) error {
	actual, err := fileSHA256(p.path(directory))
	if err != nil {
		return err
	}
	if actual != p.SHA256 {
		return fmt.Errorf("%s 的 SHA-256 与下载时不一致", p.AssetName)
	}
	return nil
}

func (m *Manager) stageDir() string {
	return m.StageDir
}

// stagedPackage reads the manifest of the staged package; a missing or malformed manifest, or no stage directory,
// means none.
func (m *Manager) stagedPackage() (stagedPackage, bool) {
	if m.StageDir == "" {
		return stagedPackage{}, false
	}
	data, err := os.ReadFile(filepath.Join(m.stageDir(), stagedManifestName))
	if err != nil {
		return stagedPackage{}, false
	}
	var staged stagedPackage
	if json.Unmarshal(data, &staged) != nil || !staged.valid() {
		return stagedPackage{}, false
	}
	return staged, true
}

// loadStaged adopts a package staged by an earlier run once per process: one newer than the running version is
// reported ready, anything else in the stage directory is removed.
func (m *Manager) loadStaged() {
	m.stageOnce.Do(func() {
		if m.StageDir == "" {
			return
		}
		staged, ok := m.stagedPackage()
		if !ok || !isNewerVersion(staged.Version, m.CurrentVersion) {
			_ = removeStaged(m.stageDir())
			return
		}
		m.mu.Lock()
		if m.state.Phase == "" || m.state.Phase == StateIdle {
			m.state = InstallState{Phase: StateReady, Version: staged.Version, ReadyVersion: staged.Version}
		}
		m.mu.Unlock()
	})
}

func (m *Manager) discardStaged() {
	if m.StageDir != "" {
		_ = removeStaged(m.stageDir())
	}
	m.mu.Lock()
	m.state.ReadyVersion = ""
	m.mu.Unlock()
}

// removeStaged removes the manifest first, so an interrupted removal never leaves a manifest without its package.
func removeStaged(directory string) error {
	err := os.Remove(filepath.Join(directory, stagedManifestName))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, os.RemoveAll(filepath.Join(directory, stagedPackageDir)))
}

// stagePackage replaces whatever is staged with archive and its manifest.
func stagePackage(directory, archive string, staged stagedPackage) error {
	if err := removeStaged(directory); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(directory, stagedPackageDir), 0o700); err != nil {
		return err
	}
	if err := os.Rename(archive, staged.path(directory)); err != nil {
		return err
	}
	data, err := json.Marshal(staged)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".staged-")
	if err != nil {
		return err
	}
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(temporary.Name())
		return err
	}
	return os.Rename(temporary.Name(), filepath.Join(directory, stagedManifestName))
}
