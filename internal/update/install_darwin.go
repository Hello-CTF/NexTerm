//go:build darwin

package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

// installBlocker reports a copy that cannot be replaced: one whose own location and /Applications are both
// unwritable for this user.
func (m *Manager) installBlocker() string {
	if m.ExePath == "" {
		return ""
	}
	appName := filepath.Base(appBundleOf(m.ExePath))
	if appName == "." || appName == "" {
		appName = "NexTerm.app"
	}
	target := macInstallTarget(m.ExePath, appName, directoryWritable)
	if err := directoryWritable(filepath.Dir(target)); err != nil {
		return fmt.Sprintf("安装目录 %s 不可写，无法在应用内更新；请下载新版 .dmg 手动安装", filepath.Dir(target))
	}
	return ""
}

// applyInstall mounts the dmg and copies its .app next to the installed bundle (see macInstallTarget), then swaps
// it in; the old bundle is removed only after the new one is in place. It returns the new bundle executable as
// the restart target.
//
// hdiutil opens the verified copy by path; the copy sits in a private directory of the stage directory, but a
// process of the same user could still replace it between the hash and the attach.
func (m *Manager) applyInstall(ctx context.Context, archive string, _ *os.File) (string, error) {
	// The package is staged in a directory that outlives the process; the mount point is a fresh temporary one.
	mountpoint, err := os.MkdirTemp(platform.TempDir(), "nexterm-update-mnt-")
	if err != nil {
		return "", err
	}
	defer os.Remove(mountpoint)
	attach := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-mountpoint", mountpoint, archive)
	if output, err := attach.CombinedOutput(); err != nil {
		return "", userErrorf(UserErrorIO, "hdiutil attach 失败: %w (%s)", err, output)
	}
	defer func() {
		_ = exec.Command("hdiutil", "detach", "-force", mountpoint).Run()
	}()
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		return "", err
	}
	appName := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".app") {
			appName = entry.Name()
			break
		}
	}
	if appName == "" {
		return "", userErrorf(UserErrorIO, "dmg 中没有 .app: %s", filepath.Base(archive))
	}
	installedName := filepath.Base(appBundleOf(m.ExePath))
	if installedName == "." || installedName == "" {
		installedName = appName
	}
	target := macInstallTarget(m.ExePath, installedName, directoryWritable)
	err = replaceBundle(target, func(staged string) error {
		copy := exec.CommandContext(ctx, "ditto", filepath.Join(mountpoint, appName), staged)
		if output, err := copy.CombinedOutput(); err != nil {
			return fmt.Errorf("ditto: %w (%s)", err, output)
		}
		return nil
	})
	if err != nil {
		return "", userErrorf(UserErrorIO, "安装 %s 到 %s 失败: %w", appName, filepath.Dir(target), err)
	}
	executable := filepath.Join(target, "Contents", "MacOS", filepath.Base(m.ExePath))
	if _, err := os.Stat(executable); err != nil {
		return m.ExePath, nil
	}
	return executable, nil
}

// checkPackage accepts the dmg as verified by its checksum: it is mounted and its .app copied only when the user
// restarts.
func (m *Manager) checkPackage(context.Context, string) error {
	return nil
}
