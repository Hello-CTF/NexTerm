//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// scheduleDeferredReplace 以隐藏窗口的 powershell 启动延迟替换脚本: 脚本等待
// 本进程退出后完成二进制切换, 失败路径自动回滚旧二进制。
func scheduleDeferredReplace(newPath, targetPath string) error {
	script, err := os.CreateTemp(filepath.Dir(newPath), ".nexterm-replace-*.ps1")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	body := renderReplaceScriptWindows(newPath, targetPath, replaceScriptDelaySeconds)
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
