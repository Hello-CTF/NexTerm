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

// applyInstall 挂载 dmg, 把 .app 复制到 /Applications 后卸载; 重启目标切换到
// 新安装的 bundle 可执行文件。
func (m *Manager) applyInstall(ctx context.Context, archive string) error {
	mountpoint := filepath.Join(filepath.Dir(archive), "mnt")
	if err := os.Mkdir(mountpoint, 0o700); err != nil {
		return err
	}
	attach := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-mountpoint", mountpoint, archive)
	if output, err := attach.CombinedOutput(); err != nil {
		return fmt.Errorf("hdiutil attach 失败: %w (%s)", err, output)
	}
	defer func() {
		_ = exec.Command("hdiutil", "detach", "-force", mountpoint).Run()
	}()
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		return err
	}
	appName := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".app") {
			appName = entry.Name()
			break
		}
	}
	if appName == "" {
		return fmt.Errorf("dmg 中没有 .app: %s", filepath.Base(archive))
	}
	target := filepath.Join("/Applications", appName)
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("清理旧应用失败: %w", err)
	}
	copy := exec.CommandContext(ctx, "ditto", filepath.Join(mountpoint, appName), target)
	if output, err := copy.CombinedOutput(); err != nil {
		return fmt.Errorf("复制 %s 到 /Applications 失败: %w (%s)", appName, err, output)
	}
	executable := filepath.Join(target, "Contents", "MacOS", filepath.Base(m.ExePath))
	if _, err := os.Stat(executable); err == nil {
		m.restartTarget = executable
	}
	return nil
}
