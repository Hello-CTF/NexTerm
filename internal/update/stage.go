package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A verified package waits in the stage directory until the restart that applies it:
//
//	<stage>/staged.json                     a pointer: version and asset name, written last
//	<stage>/package/<asset name>            the package
//	<stage>/package/SHA256SUMS              the release's exact checksum manifest
//	<stage>/package/SHA256SUMS.minisig      its signature
//
// The stage directory is writable by the user, so nothing in it is trusted on its own: before a staged package is
// reported ready or applied, the manifest's signature is verified again with the build's trusted key, bound to the
// staged version, and the package's digest is taken from that manifest. A package the app exits without applying
// stays staged and the next launch offers its restart again, unless that launch already runs the staged version or
// a newer one, or the package is a release candidate no longer on the selected channel.
const (
	stagedManifestName = "staged.json"
	stagedPackageDir   = "package"
)

// errStagedTampered marks a staged package whose files no longer match their signed checksums: a file that is
// missing, not a regular file, replaced while opened, or whose content fails verification. Only such a package is
// discarded; any other error (permissions, file handles, a Windows sharing violation) is retryable and keeps it.
var errStagedTampered = errors.New("staged package does not match its signed checksums")

// tampered classifies an error reading a staged file.
func tampered(err error) error {
	if errors.Is(err, errStagedTampered) {
		return err
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %v", errStagedTampered, err)
	}
	return err
}

type stagedPackage struct {
	Version   string `json:"version"`
	AssetName string `json:"assetName"`
}

func (p stagedPackage) path(directory string) string {
	return filepath.Join(directory, stagedPackageDir, p.AssetName)
}

// validFor requires the asset name the release would carry for this platform and the staged version.
func (p stagedPackage) validFor(goos, goarch string) bool {
	expected, ok := assetNameFor(goos, goarch, p.Version)
	return ok && p.Version != "" && p.AssetName == expected
}

func (m *Manager) stageDir() string {
	return m.StageDir
}

// stagedPackage reads the pointer to the staged package; a missing or malformed pointer, or no stage directory,
// means none.
func (m *Manager) stagedPackage() (stagedPackage, bool) {
	if m.StageDir == "" {
		return stagedPackage{}, false
	}
	data, err := readRegular(filepath.Join(m.stageDir(), stagedManifestName), 4<<10)
	if err != nil {
		return stagedPackage{}, false
	}
	var staged stagedPackage
	if json.Unmarshal(data, &staged) != nil {
		return stagedPackage{}, false
	}
	return staged, true
}

// stagedDigest verifies the staged manifest's signature, bound to the staged version, with the build's trusted key
// and returns the package's digest from it. A build without a trusted key applies nothing.
func (m *Manager) stagedDigest(staged stagedPackage) (string, error) {
	trust, required, err := m.minisignTrust()
	if err != nil {
		return "", err
	}
	if !required {
		return "", fmt.Errorf("%w: 此构建未内置发布签名公钥，不应用未签名的更新包", errStagedTampered)
	}
	directory := filepath.Join(m.stageDir(), stagedPackageDir)
	manifest, err := readRegular(filepath.Join(directory, checksumsAssetName), maxChecksumManifestSize)
	if err != nil {
		return "", tampered(err)
	}
	signature, err := readRegular(filepath.Join(directory, checksumSignatureAssetName), maxMinisignSignatureSize)
	if err != nil {
		return "", tampered(err)
	}
	sums, err := verifyChecksums(manifest, signature, trust, true, staged.Version)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errStagedTampered, err)
	}
	digest, ok := sums[staged.AssetName]
	if !ok {
		return "", fmt.Errorf("%w: %s 缺少 %s 的校验和", errStagedTampered, checksumsAssetName, staged.AssetName)
	}
	return digest, nil
}

// stagedProblem reports why a staged package must not be offered or applied, or "" when it may.
func (m *Manager) stagedProblem(ctx context.Context, staged stagedPackage) string {
	switch {
	case !staged.validFor(m.GOOS, m.GOARCH) || !releaseVersionForm(staged.Version):
		return "已下载的更新包不属于当前平台，已丢弃"
	case !isNewerVersion(staged.Version, m.CurrentVersion):
		return fmt.Sprintf("已下载的更新 %s 不比当前版本新，已丢弃", staged.Version)
	case m.offChannel(ctx, staged.Version):
		return fmt.Sprintf("已切换到稳定版通道，已丢弃下载的候选版本 %s", staged.Version)
	}
	return ""
}

// releaseVersionForm accepts a stable version or a release candidate in the rc.N form, the only prereleases
// the release check offers.
func releaseVersionForm(version string) bool {
	parsed, ok := parseVersion(version)
	return ok && (!parsed.hasPre || isRCPrerelease(parsed.pre))
}

// offChannel reports a release candidate while the stable channel is known to be selected and the running build
// is stable. A channel that cannot be read counts as not off channel: discarding a download is not worth a
// transient settings error.
func (m *Manager) offChannel(ctx context.Context, version string) bool {
	if !runningPrerelease(version) || runningPrerelease(m.CurrentVersion) {
		return false
	}
	if m.ChannelLookup != nil {
		channel, err := m.ChannelLookup(ctx)
		return err == nil && channel == ChannelStable
	}
	return m.releaseChannel(ctx) == ChannelStable
}

// loadStaged adopts a package staged by an earlier run once per process, sweeping leftovers of interrupted
// downloads first: a package that passes every check is reported ready, anything else is removed.
func (m *Manager) loadStaged() {
	m.stageOnce.Do(func() {
		if m.StageDir == "" {
			return
		}
		sweepStageLeftovers(m.stageDir())
		staged, ok := m.stagedPackage()
		if !ok || m.stagedProblem(context.Background(), staged) != "" {
			_ = removeStaged(m.stageDir())
			return
		}
		digest, err := m.stagedDigest(staged)
		if err == nil {
			err = hashMatches(staged.path(m.stageDir()), digest)
		}
		if err != nil {
			// A package that fails verification is removed; one that could not be read right now is kept for
			// the next launch, but not offered.
			if errors.Is(err, errStagedTampered) {
				_ = removeStaged(m.stageDir())
			}
			return
		}
		m.mu.Lock()
		if m.state.Phase == "" || m.state.Phase == StateIdle {
			m.state = InstallState{Phase: StateReady, Version: staged.Version, ReadyVersion: staged.Version}
		}
		m.mu.Unlock()
	})
}

// ChannelChanged discards a staged release candidate after the user selected the stable channel. It runs only on
// that explicit change (and the launch check in loadStaged), never while a download or an apply holds the stage;
// a restart still refuses the candidate then. It reports whether it discarded the package.
func (m *Manager) ChannelChanged(ctx context.Context, channel ReleaseChannel) bool {
	if channel != ChannelStable || m.StageDir == "" {
		return false
	}
	if !m.installing.CompareAndSwap(false, true) {
		return false
	}
	defer m.installing.Store(false)
	m.loadStaged()
	staged, ok := m.stagedPackage()
	if !ok || !runningPrerelease(staged.Version) || runningPrerelease(m.CurrentVersion) {
		return false
	}
	m.discardStaged()
	return true
}

func (m *Manager) discardStaged() {
	if m.StageDir != "" {
		_ = removeStaged(m.stageDir())
	}
	m.mu.Lock()
	m.state.ReadyVersion = ""
	if m.state.Phase == StateReady {
		m.state.Phase = StateIdle
	}
	m.mu.Unlock()
}

// sweepStageLeftovers removes the dot-prefixed work directories and files an interrupted run left behind.
func sweepStageLeftovers(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			_ = os.RemoveAll(filepath.Join(directory, entry.Name()))
		}
	}
}

// removeStaged removes the pointer first, so an interrupted removal never leaves a pointer without its package.
func removeStaged(directory string) error {
	err := os.Remove(filepath.Join(directory, stagedManifestName))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, os.RemoveAll(filepath.Join(directory, stagedPackageDir)))
}

// prepareStageDir creates the stage directory private to this user, tightening an existing one.
func prepareStageDir(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return os.Chmod(directory, 0o700)
}

// stagePackage replaces whatever is staged with archive, the release's checksum files and the pointer, syncing
// each before the pointer is written.
func stagePackage(directory, archive string, staged stagedPackage, fetched checksums) error {
	if err := removeStaged(directory); err != nil {
		return err
	}
	packageDir := filepath.Join(directory, stagedPackageDir)
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		return err
	}
	if err := syncFile(archive); err != nil {
		return err
	}
	if err := os.Rename(archive, staged.path(directory)); err != nil {
		return err
	}
	if err := writeSynced(packageDir, checksumsAssetName, fetched.manifest); err != nil {
		return err
	}
	if len(fetched.signature) > 0 {
		if err := writeSynced(packageDir, checksumSignatureAssetName, fetched.signature); err != nil {
			return err
		}
	}
	syncDir(packageDir)
	data, err := json.Marshal(staged)
	if err != nil {
		return err
	}
	if err := writeSynced(directory, stagedManifestName, data); err != nil {
		return err
	}
	syncDir(directory)
	return nil
}

// writeSynced writes data to directory/name through a synced temporary file and a rename.
func writeSynced(directory, name string, data []byte) error {
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
	return os.Rename(temporary.Name(), filepath.Join(directory, name))
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

// syncDir makes renames in directory durable where the platform supports syncing a directory.
func syncDir(directory string) {
	if file, err := os.Open(directory); err == nil {
		_ = file.Sync()
		_ = file.Close()
	}
}

// openRegular opens path only when it is a regular file reached without following a symbolic link, and checks the
// opened file is the one that was inspected. A file that is not regular or was replaced is errStagedTampered.
func openRegular(path string) (*os.File, error) {
	inspected, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !inspected.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s 不是普通文件", errStagedTampered, filepath.Base(path))
	}
	file, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, syscallELOOP) {
			return nil, fmt.Errorf("%w: %s 是符号链接", errStagedTampered, filepath.Base(path))
		}
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(inspected, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s 在打开时被替换", errStagedTampered, filepath.Base(path))
	}
	return file, nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	file, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readReleaseAsset(file, filepath.Base(path), limit)
}

// hashMatches hashes the staged package through openRegular and compares it with digest.
func hashMatches(path, digest string) error {
	file, err := openRegular(path)
	if err != nil {
		return tampered(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("%w: %s 的 SHA-256 与签名的校验和不一致", errStagedTampered, filepath.Base(path))
	}
	return nil
}

// copyVerified opens the staged package once and copies it into a fresh private directory under parent, then
// reopens the copy through openVerifiedCopy, hashes it through that handle and returns the handle, rewound, only
// when the hash matches digest. The Linux installer reads that handle. On Windows the handle denies writing,
// deleting and renaming the copy to everyone while it is open, so the file NSIS runs is the file that was hashed.
// hdiutil on macOS opens the copy by path in another process, and macOS has no mandatory lock: the copy is
// verified after it was written and sits in a directory only this user can write, but a process of the same user
// could still replace it between the hash and the attach. cleanup closes and removes the copy.
func copyVerified(path, parent, digest string) (*os.File, func(), error) {
	directory, err := os.MkdirTemp(parent, ".apply-")
	if err != nil {
		return nil, nil, err
	}
	copyPath := filepath.Join(directory, filepath.Base(path))
	if err := copyStaged(path, copyPath); err != nil {
		_ = os.RemoveAll(directory)
		return nil, nil, err
	}
	copied, err := openVerifiedCopy(copyPath)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, nil, err
	}
	cleanup := func() {
		_ = copied.Close()
		_ = os.RemoveAll(directory)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, copied); err != nil {
		cleanup()
		return nil, nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		cleanup()
		return nil, nil, fmt.Errorf("%w: %s 的 SHA-256 与签名的校验和不一致", errStagedTampered, filepath.Base(path))
	}
	if _, err := copied.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return copied, cleanup, nil
}

// copyStaged copies the staged package, opened through openRegular, to a new file at copyPath and syncs it. The
// write handle is closed before the copy is reopened for verification.
func copyStaged(path, copyPath string) error {
	source, err := openRegular(path)
	if err != nil {
		return tampered(err)
	}
	defer source.Close()
	target, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	syncErr := target.Sync()
	return errors.Join(copyErr, syncErr, target.Close())
}
