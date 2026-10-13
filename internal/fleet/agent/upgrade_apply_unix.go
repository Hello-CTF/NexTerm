//go:build unix

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// scheduleDeferredReplace 以独立会话启动延迟替换脚本: 脚本等待本进程退出后
// 完成二进制切换, 失败路径自动回滚旧二进制。setsid 让脚本逃脱 launchd 对
// 同进程组残留进程的清理; systemd 按 cgroup 清理, setsid 无效, 故 linux 且
// 运行于 systemd 下 (INVOCATION_ID 存在) 时经 systemd-run --scope 把脚本放进
// 独立的 scope cgroup, 保证 agent 单元停止后脚本仍能完成替换并拉起服务。
func scheduleDeferredReplace(newPath, targetPath string) error {
	script, err := os.CreateTemp(filepath.Dir(newPath), ".nexterm-replace-*.sh")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	body := renderReplaceScriptUnix(newPath, targetPath, replaceScriptDelaySeconds, unixRestartCommand(runtime.GOOS))
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Chmod(0o755); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	if runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "" {
		cmd = exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "/bin/sh", scriptPath)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
