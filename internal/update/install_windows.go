//go:build windows

package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/windows/registry"
)

// applyInstall 运行 NSIS 静默安装 (/S, MUI2 页面在静默模式下自动跳过)。
// 当前运行的就是安装目标时, NSIS 无法覆盖正在运行的可执行文件,
// 先把当前 exe 改名为 .old, 安装失败时回滚; 散装副本不动原文件, 装完重启到安装版。
func (m *Manager) applyInstall(ctx context.Context, archive string) error {
	target, inPlace := installTargetFor(m.ExePath, registeredInstallTarget(), defaultInstallDir(), canonicalExeName())
	if !inPlace {
		if err := runInstaller(ctx, archive); err != nil {
			return err
		}
		if _, err := os.Stat(target); err != nil {
			return fmt.Errorf("安装完成后未找到新可执行文件 %s", target)
		}
		m.restartTarget = target
		return nil
	}
	executable := m.ExePath
	backup := executable + ".old"
	if err := os.Rename(executable, backup); err != nil {
		return fmt.Errorf("备份当前可执行文件失败: %w", err)
	}
	if err := runInstaller(ctx, archive); err != nil {
		_ = os.Rename(backup, executable)
		return err
	}
	if _, err := os.Stat(executable); err != nil {
		_ = os.Rename(backup, executable)
		return fmt.Errorf("安装完成后未找到新可执行文件 %s", executable)
	}
	m.restartTarget = executable
	return nil
}

func runInstaller(ctx context.Context, archive string) error {
	command := exec.CommandContext(ctx, archive, "/S")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("NSIS 静默安装失败: %w (%s)", err, output)
	}
	return nil
}

func canonicalExeName() string {
	return "nexterm-desktop-windows-" + runtime.GOARCH + ".exe"
}

func defaultInstallDir() string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return ""
	}
	return filepath.Join(local, "Programs", "NexTerm")
}

func registeredInstallTarget() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\NexTerm`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = key.Close() }()
	dir, _, err := key.GetStringValue("InstallDir")
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, canonicalExeName())
}
