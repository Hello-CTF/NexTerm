//go:build windows

package agent

import (
	"context"
	"fmt"
	"strings"
)

const windowsServiceName = "NexTermAgent"

type windowsManager struct {
	runner CommandRunner
}

func newWindowsManager(runner CommandRunner) ServiceManager {
	return &windowsManager{runner: runner}
}

func (m *windowsManager) Install(ctx context.Context, executable, dataDir string) error {
	_, _ = m.runner.Run(ctx, "sc.exe", "stop", windowsServiceName)
	_, _ = m.runner.Run(ctx, "sc.exe", "delete", windowsServiceName)
	binPath := "\"" + executable + "\" run --data-dir \"" + dataDir + "\""
	output, err := m.runner.Run(ctx, "sc.exe", "create", windowsServiceName, "binPath=", binPath, "start=", "auto")
	if err != nil {
		return fmt.Errorf("sc create: %w", scHint(output, err))
	}
	output, err = m.runner.Run(ctx, "sc.exe", "start", windowsServiceName)
	if err != nil {
		return fmt.Errorf("sc start: %w", scHint(output, err))
	}
	return nil
}

func (m *windowsManager) Uninstall(ctx context.Context) error {
	if output, err := m.runner.Run(ctx, "sc.exe", "stop", windowsServiceName); err != nil && !scMissing(trimOutput(output)) && !scNotRunning(trimOutput(output)) {
		return fmt.Errorf("sc stop: %w", err)
	}
	if output, err := m.runner.Run(ctx, "sc.exe", "delete", windowsServiceName); err != nil && !scMissing(trimOutput(output)) {
		return fmt.Errorf("sc delete: %w", err)
	}
	return nil
}

func (m *windowsManager) Status(ctx context.Context) ServiceState {
	status := ServiceState{}
	output, err := m.runner.Run(ctx, "sc.exe", "query", windowsServiceName)
	trimmed := trimOutput(output)
	if err != nil {
		if scMissing(trimmed) {
			return status
		}
		status.LastError = scFailure(trimmed, err)
		return status
	}
	state, failure := scState(trimmed)
	if failure != "" {
		status.LastError = failure
		return status
	}
	status.Installed = true
	status.Active = state == "RUNNING"
	output, err = m.runner.Run(ctx, "sc.exe", "qc", windowsServiceName)
	trimmed = trimOutput(output)
	if err != nil {
		status.LastError = scFailure(trimmed, err)
		return status
	}
	startType, failure := scStartType(trimmed)
	if failure != "" {
		status.LastError = failure
		return status
	}
	status.Enabled = startType == "AUTO_START"
	return status
}

// sc 错误码数字跨语言环境稳定 (自由文本消息会被本地化): 5=拒绝访问, 1060=服务不存在, 1062=服务未运行。
func scHint(output string, err error) error {
	if strings.Contains(output, "FAILED 5") {
		return fmt.Errorf("%w (需要管理员权限, 请以管理员身份运行)", err)
	}
	return err
}

func scMissing(output string) bool {
	return strings.Contains(output, "FAILED 1060")
}

func scNotRunning(output string) bool {
	return strings.Contains(output, "FAILED 1062")
}

var windowsServiceStates = map[string]bool{
	"STOPPED": true, "START_PENDING": true, "STOP_PENDING": true,
	"RUNNING": true, "CONTINUE_PENDING": true, "PAUSE_PENDING": true,
	"PAUSED": true,
}

var windowsStartTypes = map[string]bool{
	"BOOT_START": true, "SYSTEM_START": true, "AUTO_START": true,
	"DEMAND_START": true, "DISABLED": true,
}

// scState 从 sc.exe query 输出解析 STATE 行 (形如 "STATE : 4  RUNNING"),
// 状态词是该行最后一个字段; 找不到或状态未知即解析失败。
func scState(output string) (string, string) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "STATE") {
			continue
		}
		_, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		if state := fields[len(fields)-1]; windowsServiceStates[state] {
			return state, ""
		}
	}
	return "", "sc query: 输出中未找到可识别的 STATE"
}

// scStartType 从 sc.exe qc 输出解析 START_TYPE 行 (形如
// "START_TYPE : 2   AUTO_START", 延迟自启为 "2   AUTO_START  (DELAYED)"),
// 数字代码后第一个字段即类型名; 找不到或类型未知即解析失败。
func scStartType(output string) (string, string) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "START_TYPE") {
			continue
		}
		_, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) < 2 {
			continue
		}
		if startType := fields[1]; windowsStartTypes[startType] {
			return startType, ""
		}
	}
	return "", "sc qc: 输出中未找到可识别的 START_TYPE"
}

func scFailure(output string, err error) string {
	if output != "" {
		return output
	}
	return err.Error()
}
