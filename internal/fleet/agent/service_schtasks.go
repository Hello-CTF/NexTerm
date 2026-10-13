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

// windowsTaskName 同时是计划任务名与 v1.x 旧系统服务 (sc.exe) 名, 迁移时按同名清理。
const windowsTaskName = "NexTermAgent"

// windowsManager 把 agent 自启注册为当前用户的计划任务: LogonTrigger 登录即起,
// LogonType S4U (不存密码的"无论是否登录都运行") 让进程跑在非交互 window station,
// console 子系统二进制也不会出现 console 窗口; 注册/删除不需要管理员权限。
type windowsManager struct {
	runner        CommandRunner
	commandPrefix []string
}

func newWindowsManager(runner CommandRunner, commandPrefix ...string) ServiceManager {
	return &windowsManager{runner: runner, commandPrefix: commandPrefix}
}

// windowsTaskUser 返回计划任务的运行账户 (DOMAIN\user); 测试可替换。
var windowsTaskUser = func() (string, error) {
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
	account, err := windowsTaskUser()
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
	// /End 尽力而为: 任务不存在或未在运行都继续走删除, 真实失败由 /Delete 暴露。
	_, _ = m.runner.Run(ctx, "schtasks.exe", "/End", "/TN", windowsTaskName)
	output, err := m.runner.Run(ctx, "schtasks.exe", "/Delete", "/TN", windowsTaskName, "/F")
	if err != nil && !windowsTaskMissing(trimOutput(output)) {
		return fmt.Errorf("删除计划任务失败: %w", schtasksHint(trimOutput(output), err))
	}
	return m.removeLegacyService(ctx)
}

func (m *windowsManager) Status(ctx context.Context) ServiceState {
	status := ServiceState{}
	_, err := m.runner.Run(ctx, "schtasks.exe", "/Query", "/TN", windowsTaskName, "/FO", "CSV", "/NH")
	if err != nil {
		// 任务不存在时 /Query 以非零码退出, 视为未安装; 只有 schtasks.exe
		// 本身不可执行 (非退出码错误) 才是需要暴露的环境故障。
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
	// Get-ScheduledTask 的 State 是枚举, 名字不随系统区域设置本地化。
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

// windowsTaskStateCommand 以退出码区分任务不存在 (Get-ScheduledTask 报错) 与读到状态。
const windowsTaskStateCommand = "(Get-ScheduledTask -TaskName '" + windowsTaskName + "').State.ToString()"

// removeLegacyService 清除 v1.x 用 sc.exe 注册的同名系统服务 (LocalSystem, 需管理员);
// 服务不存在 (1060) 与未运行 (1062) 属正常, 其余失败说明旧部署仍在, 必须报错而不是放过。
func (m *windowsManager) removeLegacyService(ctx context.Context) error {
	if output, err := m.runner.Run(ctx, "sc.exe", "stop", windowsTaskName); err != nil && !scMissing(trimOutput(output)) && !scNotRunning(trimOutput(output)) {
		return fmt.Errorf("检测到旧版系统服务 %s, 停止失败 (需要管理员权限, 请以管理员身份重跑一次完成迁移): %w", windowsTaskName, err)
	}
	if output, err := m.runner.Run(ctx, "sc.exe", "delete", windowsTaskName); err != nil && !scMissing(trimOutput(output)) {
		return fmt.Errorf("检测到旧版系统服务 %s, 删除失败 (需要管理员权限, 请以管理员身份重跑一次完成迁移): %w", windowsTaskName, err)
	}
	return nil
}

// sc 错误码数字跨语言环境稳定 (自由文本消息会被本地化): 1060=服务不存在, 1062=服务未运行。
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

// schtasksHint 把 schtasks 失败翻译成可操作文案: 工具缺失 (非退出码错误) 与
// 拒绝访问 (输出跨语言环境匹配 EN/ZH 两种固定文案) 分开指向, 其余原样带回输出。
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

// writeWindowsTaskXML 把任务定义写到临时文件; schtasks /Create /XML 只接受
// 带 BOM 的 UTF-16LE 文件。
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
