//go:build darwin

package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func (m *Manager) applyInstall(ctx context.Context, archive string) (string, error) {
	mountpoint := filepath.Join(filepath.Dir(archive), "mnt")
	if err := os.Mkdir(mountpoint, 0o700); err != nil {
		return "", err
	}
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
