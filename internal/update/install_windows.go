//go:build windows

package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/windows/registry"
)

// applyInstall runs the NSIS silent install (/S; MUI2 pages are auto-skipped in silent mode).
// When the currently running exe is the install target, NSIS cannot overwrite the running executable,
// so the current exe is first renamed to .old and rolled back on install failure; loose copies leave the original file alone and restart into the installed version.
func (m *Manager) applyInstall(ctx context.Context, archive string) error {
	target, inPlace := installTargetFor(m.ExePath, registeredInstallTarget(), defaultInstallDir(), canonicalExeName())
	if !inPlace {
		if err := runInstaller(ctx, archive); err != nil {
			return err
		}
		if _, err := os.Stat(target); err != nil {
			return userErrorf(UserErrorIO, "安装完成后未找到新可执行文件 %s", target)
		}
		m.restartTarget = target
		return nil
	}
	executable := m.ExePath
	backup := executable + ".old"
	if err := os.Rename(executable, backup); err != nil {
		return userErrorf(UserErrorIO, "备份当前可执行文件失败: %w", err)
	}
	if err := runInstaller(ctx, archive); err != nil {
		_ = os.Rename(backup, executable)
		return err
	}
	if _, err := os.Stat(executable); err != nil {
		_ = os.Rename(backup, executable)
		return userErrorf(UserErrorIO, "安装完成后未找到新可执行文件 %s", executable)
	}
	m.restartTarget = executable
	return nil
}

func runInstaller(ctx context.Context, archive string) error {
	command := exec.CommandContext(ctx, archive, "/S")
	if output, err := command.CombinedOutput(); err != nil {
		return userErrorf(UserErrorIO, "NSIS 静默安装失败: %w (%s)", err, output)
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
