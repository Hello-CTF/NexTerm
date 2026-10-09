//go:build windows

package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// applyInstall 运行 NSIS 静默安装 (/S, MUI2 页面在静默模式下自动跳过)。
// NSIS 无法覆盖正在运行的可执行文件, 先把当前 exe 改名为 .old, 安装失败时回滚。
func (m *Manager) applyInstall(ctx context.Context, archive string) error {
	executable := m.ExePath
	backup := executable + ".old"
	if err := os.Rename(executable, backup); err != nil {
		return fmt.Errorf("备份当前可执行文件失败: %w", err)
	}
	command := exec.CommandContext(ctx, archive, "/S")
	if output, err := command.CombinedOutput(); err != nil {
		_ = os.Rename(backup, executable)
		return fmt.Errorf("NSIS 静默安装失败: %w (%s)", err, output)
	}
	if _, err := os.Stat(executable); err != nil {
		_ = os.Rename(backup, executable)
		return fmt.Errorf("安装完成后未找到新可执行文件 %s", executable)
	}
	m.restartTarget = executable
	return nil
}
