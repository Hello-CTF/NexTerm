package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"unicode/utf16"
)

// windowsTaskName is both the scheduled task name and the v1.x legacy system service (sc.exe) name; migration cleans up by the same name.
const windowsTaskName = "NexTermAgent"

// windowsManager registers agent autostart as a scheduled task for the current user: LogonTrigger starts it at logon,
// LogonType S4U ("run whether user is logged on or not" without storing a password) lets the process run in a non-interactive window station,
// and console-subsystem binaries show no console window; registration/removal needs no admin rights.
type windowsManager struct {
	runner        CommandRunner
	commandPrefix []string
	taskUser      func() (string, error)
}

func newWindowsManager(runner CommandRunner, commandPrefix ...string) *windowsManager {
	return &windowsManager{runner: runner, commandPrefix: commandPrefix, taskUser: windowsTaskUser}
}

func windowsTaskUser() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	if current.Username == "" {
		return "", errors.New("当前用户名为空")
	}
	return current.Username, nil
}

func (m *windowsManager) Install(ctx context.Context, executable, dataDir string) error {
	if err := m.removeLegacyService(ctx); err != nil {
		return err
	}
	account, err := m.taskUser()
	if err != nil {
		return fmt.Errorf("读取当前用户失败: %w", err)
	}
	xmlPath, err := writeWindowsTaskXML(windowsTaskXML(account, executable, dataDir, m.commandPrefix))
	if err != nil {
		return fmt.Errorf("生成计划任务定义失败: %w", err)
	}
	defer os.Remove(xmlPath)
	output, err := m.runner.Run(ctx, "schtasks.exe", "/Create", "/TN", windowsTaskName, "/XML", xmlPath, "/F")
	if err != nil {
		return fmt.Errorf("注册计划任务失败: %w", schtasksHint(trimOutput(output), err))
	}
	output, err = m.runner.Run(ctx, "schtasks.exe", "/Run", "/TN", windowsTaskName)
	if err != nil {
		return fmt.Errorf("启动计划任务失败: %w", schtasksHint(trimOutput(output), err))
	}
	return nil
}

func (m *windowsManager) Uninstall(ctx context.Context) error {
	// /End is best-effort: a missing or non-running task both continue to deletion; real failures surface from /Delete.
	_, _ = m.runner.Run(ctx, "schtasks.exe", "/End", "/TN", windowsTaskName)
	output, err := m.runner.Run(ctx, "schtasks.exe", "/Delete", "/TN", windowsTaskName, "/F")
	if err != nil && !windowsTaskMissing(trimOutput(output)) {
		// The "task does not exist" wording is not enumerable across non-EN/ZH locales, so after a failed /Delete re-check:
		// task already gone (exit-code failure) counts as deleted; schtasks itself being unavailable
		// (a non-exit-code error) is not covered by this fallback.
		if _, queryErr := m.runner.Run(ctx, "schtasks.exe", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH"); queryErr != nil && isExitError(queryErr) {
			return m.removeLegacyService(ctx)
		}
		return fmt.Errorf("删除计划任务失败: %w", schtasksHint(trimOutput(output), err))
	}
	return m.removeLegacyService(ctx)
}

func (m *windowsManager) Status(ctx context.Context) ServiceState {
	status := ServiceState{}
	_, err := m.runner.Run(ctx, "schtasks.exe", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH")
	if err != nil {
		// When the task does not exist, /Query exits with a non-zero code, which counts as not installed; only schtasks.exe
		// itself being unexecutable (a non-exit-code error) is an environment fault worth surfacing.
		if !isExitError(err) {
			status.LastError = err.Error()
		}
		return status
	}
	status.Installed = true
	output, err := m.runner.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsTaskStateCommand)
	trimmed := trimOutput(output)
	if err != nil {
		status.LastError = failureText(trimmed, err)
		return status
	}
	// Get-ScheduledTask's State is an enum whose names are not localized with the system locale.
	switch trimmed {
	case "Running":
		status.Enabled = true
		status.Active = true
	case "Ready", "Queued":
		status.Enabled = true
	case "Disabled":
	default:
		status.LastError = "无法识别的计划任务状态: " + trimmed
	}
	return status
}

// windowsTaskStateCommand distinguishes a missing task (Get-ScheduledTask errors) from a successfully read state via the exit code.
const windowsTaskStateCommand = "(Get-ScheduledTask -TaskName '" + windowsTaskName + "').State.ToString()"

// removeLegacyService removes the same-named system service registered with sc.exe by v1.x (LocalSystem, requires admin);
// service missing (1060) and not running (1062) are normal; any other failure means the old deployment is still present and must error rather than be let through.
func (m *windowsManager) removeLegacyService(ctx context.Context) error {
	if output, err := m.runner.Run(ctx, "sc.exe", "stop", windowsTaskName); err != nil && !scMissing(trimOutput(output)) && !scNotRunning(trimOutput(output)) {
		return fmt.Errorf("检测到旧版系统服务 %s, 停止失败 (需要管理员权限, 请以管理员身份重跑一次完成迁移): %w", windowsTaskName, err)
	}
	if output, err := m.runner.Run(ctx, "sc.exe", "delete", windowsTaskName); err != nil && !scMissing(trimOutput(output)) {
		return fmt.Errorf("检测到旧版系统服务 %s, 删除失败 (需要管理员权限, 请以管理员身份重跑一次完成迁移): %w", windowsTaskName, err)
	}
	return nil
}

// sc error code numbers are stable across locales (the free-text messages get localized): 1060=service does not exist, 1062=service not running.
func scMissing(output string) bool {
	return strings.Contains(output, "FAILED 1060")
}

func scNotRunning(output string) bool {
	return strings.Contains(output, "FAILED 1062")
}

func isExitError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

func windowsTaskMissing(output string) bool {
	return strings.Contains(output, "cannot find the file specified") || strings.Contains(output, "找不到指定的文件")
}

// schtasksHint translates schtasks failures into actionable wording: tool missing (non-exit-code error) and
// access denied (matching the two fixed EN/ZH wordings in the output across locales) are pointed out separately; anything else passes the output through as-is.
func schtasksHint(output string, err error) error {
	if !isExitError(err) {
		return fmt.Errorf("%w (schtasks.exe 不可用, 无法管理计划任务)", err)
	}
	if strings.Contains(output, "Access is denied") || strings.Contains(output, "拒绝访问") {
		return fmt.Errorf("%w (拒绝访问: 当前用户无权管理计划任务, 请检查安全策略)", err)
	}
	if output != "" {
		return fmt.Errorf("%w: %s", err, output)
	}
	return err
}

func failureText(output string, err error) string {
	if output != "" {
		return output
	}
	return err.Error()
}

func windowsTaskXML(account, executable, dataDir string, commandPrefix []string) string {
	args := make([]string, 0, len(commandPrefix)+3)
	args = append(args, commandPrefix...)
	args = append(args, "run", "--data-dir", dataDir)
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, windowsQuoteArg(arg))
	}
	return fmt.Sprintf(windowsTaskXMLTemplate,
		windowsXMLEscape(account),
		windowsXMLEscape(windowsQuoteArg(executable)),
		windowsXMLEscape(strings.Join(quoted, " ")))
}

func windowsQuoteArg(s string) string {
	if strings.ContainsAny(s, " \t") {
		return "\"" + s + "\""
	}
	return s
}

var windowsXMLReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func windowsXMLEscape(s string) string {
	return windowsXMLReplacer.Replace(s)
}

// writeWindowsTaskXML writes the task definition to a temp file; schtasks /Create /XML only accepts
// UTF-16LE files with a BOM.
func writeWindowsTaskXML(xmlDoc string) (string, error) {
	file, err := os.CreateTemp("", "nexterm-agent-task-*.xml")
	if err != nil {
		return "", err
	}
	if _, err := file.Write(windowsUTF16LE(xmlDoc)); err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func windowsUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	buf := make([]byte, 2+2*len(u))
	buf[0] = 0xFF
	buf[1] = 0xFE
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[2+2*i:], c)
	}
	return buf
}

const windowsTaskXMLTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>NexTerm device agent</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>S4U</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Count>3</Count>
      <Interval>PT1M</Interval>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>
`
