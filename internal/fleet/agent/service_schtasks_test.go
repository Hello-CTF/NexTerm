package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func newTestWindowsManager(runner CommandRunner, commandPrefix ...string) *windowsManager {
	manager := newWindowsManager(runner, commandPrefix...)
	manager.taskUser = func() (string, error) { return `DESKTOP\dev`, nil }
	return manager
}

// xmlCaptureRunner captures the XML file content passed to schtasks on /Create calls
// (the temp file is deleted once Install returns); all other commands go to fakeRunner.
type xmlCaptureRunner struct {
	*fakeRunner
	xml         []byte
	createOut   string
	createErr   error
	createCalls int
}

func (r *xmlCaptureRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "schtasks.exe" && len(args) == 6 && args[0] == "/Create" {
		r.commands = append(r.commands, recordedCommand{name: name, args: args})
		r.createCalls++
		if r.createErr != nil {
			return r.createOut, r.createErr
		}
		data, err := os.ReadFile(args[4])
		if err != nil {
			return "", err
		}
		r.xml = data
		return "", nil
	}
	return r.fakeRunner.Run(ctx, name, args...)
}

func utf16LEToString(t *testing.T, data []byte) string {
	t.Helper()
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xFE {
		t.Fatalf("task XML must start with a UTF-16LE BOM, got % x (len %d)", data[:min(4, len(data))], len(data))
	}
	u16 := make([]uint16, 0, (len(data)-2)/2)
	for i := 2; i+1 < len(data); i += 2 {
		u16 = append(u16, uint16(data[i])|uint16(data[i+1])<<8)
	}
	return string(utf16.Decode(u16))
}

func TestWindowsTaskXMLStructure(t *testing.T) {
	xml := windowsTaskXML(`DESKTOP\dev`, `C:\Program Files\NexTerm\nexterm-agent.exe`, `C:\Users\dev user\NexTerm`, nil)
	for _, expected := range []string{
		"<LogonTrigger>",
		"<UserId>DESKTOP\\dev</UserId>",
		"<LogonType>S4U</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<RestartOnFailure>",
		"<Count>3</Count>",
		"<Interval>PT1M</Interval>",
		`<Command>"C:\Program Files\NexTerm\nexterm-agent.exe"</Command>`,
		`<Arguments>run --data-dir "C:\Users\dev user\NexTerm"</Arguments>`,
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
	} {
		if !strings.Contains(xml, expected) {
			t.Fatalf("task XML is missing %q:\n%s", expected, xml)
		}
	}
	if strings.Contains(xml, "InteractiveToken") {
		t.Fatalf("InteractiveToken would show a console window for console-subsystem processes; task XML must not contain it:\n%s", xml)
	}
}

func TestWindowsTaskXMLEscapesSpecialCharacters(t *testing.T) {
	xml := windowsTaskXML(`DOM\a&b`, `C:\agent.exe`, `C:\data\<x>&y`, nil)
	for _, expected := range []string{
		`<UserId>DOM\a&amp;b</UserId>`,
		`<Arguments>run --data-dir C:\data\&lt;x&gt;&amp;y</Arguments>`,
	} {
		if !strings.Contains(xml, expected) {
			t.Fatalf("escaped task XML is missing %q:\n%s", expected, xml)
		}
	}
}

func TestWindowsTaskXMLIncludesCommandPrefix(t *testing.T) {
	xml := windowsTaskXML(`DESKTOP\dev`, `C:\NexTerm\nexterm-server.exe`, `C:\data`, []string{"agent"})
	if !strings.Contains(xml, `<Arguments>agent run --data-dir C:\data</Arguments>`) {
		t.Fatalf("task XML for embedded distribution must include the agent prefix:\n%s", xml)
	}
}

func TestWindowsUTF16LEEncoding(t *testing.T) {
	encoded := windowsUTF16LE("A中")
	expected := []byte{0xFF, 0xFE, 0x41, 0x00, 0x2D, 0x4E}
	if string(encoded) != string(expected) {
		t.Fatalf("windowsUTF16LE = % x, want % x", encoded, expected)
	}
}

func TestSchtasksInstallCommandsAndXML(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string]string{
			"sc.exe stop NexTermAgent":           "[SC] ControlService FAILED 1062:\n",
			"sc.exe delete NexTermAgent":         "[SC] OpenService FAILED 1060:\n",
			"schtasks.exe /Run /TN NexTermAgent": "",
		},
		errors: map[string]error{
			"sc.exe stop NexTermAgent":   realExitError(t),
			"sc.exe delete NexTermAgent": realExitError(t),
		},
	}
	capture := &xmlCaptureRunner{fakeRunner: runner}
	manager := newTestWindowsManager(capture)
	exe := `C:\Program Files\NexTerm\nexterm-agent.exe`
	dataDir := `C:\Users\dev\AppData\Local\NexTerm`
	if err := manager.Install(context.Background(), exe, dataDir); err != nil {
		t.Fatal(err)
	}
	if len(capture.commands) != 4 {
		t.Fatalf("install commands = %+v", capture.commands)
	}
	if capture.commands[0].name != "sc.exe" || strings.Join(capture.commands[0].args, " ") != "stop NexTermAgent" {
		t.Fatalf("first step must stop the legacy system service: %+v", capture.commands[0])
	}
	if capture.commands[1].name != "sc.exe" || strings.Join(capture.commands[1].args, " ") != "delete NexTermAgent" {
		t.Fatalf("second step must delete the legacy system service: %+v", capture.commands[1])
	}
	create := capture.commands[2]
	if create.name != "schtasks.exe" || create.args[0] != "/Create" || create.args[1] != "/TN" || create.args[2] != "NexTermAgent" || create.args[3] != "/XML" || create.args[5] != "/F" {
		t.Fatalf("registration command has an invalid shape: %+v", create)
	}
	if last := capture.commands[3]; last.name != "schtasks.exe" || strings.Join(last.args, " ") != "/Run /TN NexTermAgent" {
		t.Fatalf("task must start immediately after registration: %+v", last)
	}
	xml := utf16LEToString(t, capture.xml)
	for _, expected := range []string{
		"<LogonType>S4U</LogonType>",
		"<UserId>DESKTOP\\dev</UserId>",
		`<Command>"C:\Program Files\NexTerm\nexterm-agent.exe"</Command>`,
		`<Arguments>run --data-dir C:\Users\dev\AppData\Local\NexTerm</Arguments>`,
	} {
		if !strings.Contains(xml, expected) {
			t.Fatalf("written task XML is missing %q:\n%s", expected, xml)
		}
	}
	if _, err := os.Stat(create.args[4]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary XML file must be removed: %v", err)
	}
}

func TestSchtasksInstallMigratesLegacyService(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string]string{
			"sc.exe stop NexTermAgent":           "",
			"sc.exe delete NexTermAgent":         "[SC] DeleteService SUCCESS\n",
			"schtasks.exe /Run /TN NexTermAgent": "",
		},
	}
	capture := &xmlCaptureRunner{fakeRunner: runner}
	manager := newTestWindowsManager(capture)
	if err := manager.Install(context.Background(), `C:\NexTerm\nexterm-agent.exe`, `C:\NexTerm`); err != nil {
		t.Fatal(err)
	}
	if capture.createCalls != 1 {
		t.Fatalf("scheduled task must be registered after legacy service cleanup, createCalls = %d", capture.createCalls)
	}

	runner.responses["sc.exe delete NexTermAgent"] = "[SC] OpenService FAILED 5:\n\nAccess is denied.\n"
	runner.errors = map[string]error{"sc.exe delete NexTermAgent": realExitError(t)}
	err := manager.Install(context.Background(), `C:\NexTerm\nexterm-agent.exe`, `C:\NexTerm`)
	if err == nil || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("legacy service deletion denial must mention administrator privileges, got %v", err)
	}
	if capture.createCalls != 1 {
		t.Fatal("scheduled task must not be registered before legacy service cleanup")
	}
}

func TestSchtasksInstallReportsCreateAndRunFailures(t *testing.T) {
	baseResponses := func() map[string]string {
		return map[string]string{
			"sc.exe stop NexTermAgent":   "[SC] OpenService FAILED 1060:\n",
			"sc.exe delete NexTermAgent": "[SC] OpenService FAILED 1060:\n",
		}
	}
	baseErrors := func() map[string]error {
		return map[string]error{
			"sc.exe stop NexTermAgent":   realExitError(t),
			"sc.exe delete NexTermAgent": realExitError(t),
		}
	}

	capture := &xmlCaptureRunner{fakeRunner: &fakeRunner{responses: baseResponses(), errors: baseErrors()}}
	capture.createOut, capture.createErr = "ERROR: Access is denied.\n", realExitError(t)
	err := newTestWindowsManager(capture).Install(context.Background(), `C:\NexTerm\nexterm-agent.exe`, `C:\NexTerm`)
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") {
		t.Fatalf("registration denial must be actionable, got %v", err)
	}

	capture = &xmlCaptureRunner{fakeRunner: &fakeRunner{responses: baseResponses(), errors: baseErrors()}}
	capture.createErr = errors.New(`exec: "schtasks.exe": executable file not found in %PATH%`)
	err = newTestWindowsManager(capture).Install(context.Background(), `C:\NexTerm\nexterm-agent.exe`, `C:\NexTerm`)
	if err == nil || !strings.Contains(err.Error(), "schtasks.exe 不可用") {
		t.Fatalf("missing schtasks must be reported explicitly, got %v", err)
	}

	runner := &fakeRunner{responses: baseResponses(), errors: baseErrors()}
	runner.responses["schtasks.exe /Run /TN NexTermAgent"] = "ERROR: ...\n"
	runner.errors["schtasks.exe /Run /TN NexTermAgent"] = realExitError(t)
	err = newTestWindowsManager(&xmlCaptureRunner{fakeRunner: runner}).Install(context.Background(), `C:\NexTerm\nexterm-agent.exe`, `C:\NexTerm`)
	if err == nil || !strings.Contains(err.Error(), "启动计划任务失败") {
		t.Fatalf("task start failure must be reported, got %v", err)
	}
}

func TestSchtasksStatusStates(t *testing.T) {
	queryKey := "schtasks.exe /Query /TN NexTermAgent /FO CSV /NH"
	psKey := "powershell.exe -NoProfile -NonInteractive -Command " + windowsTaskStateCommand
	runner := &fakeRunner{responses: map[string]string{queryKey: "", psKey: "Running\r\n"}}
	manager := newTestWindowsManager(runner)
	ctx := context.Background()

	status := manager.Status(ctx)
	if !status.Installed || !status.Enabled || !status.Active || status.LastError != "" {
		t.Fatalf("running status = %+v", status)
	}

	runner.responses[psKey] = "Ready\n"
	status = manager.Status(ctx)
	if !status.Installed || !status.Enabled || status.Active || status.LastError != "" {
		t.Fatalf("ready status = %+v", status)
	}

	runner.responses[psKey] = "Disabled\n"
	status = manager.Status(ctx)
	if !status.Installed || status.Enabled || status.Active || status.LastError != "" {
		t.Fatalf("disabled status = %+v", status)
	}

	runner.responses[psKey] = "Weird\n"
	status = manager.Status(ctx)
	if status.LastError == "" || !strings.Contains(status.LastError, "无法识别") {
		t.Fatalf("unknown state must be reported: %+v", status)
	}

	runner.errors = map[string]error{psKey: realExitError(t)}
	runner.responses[psKey] = "Get-ScheduledTask : 系统找不到指定的文件。\n"
	status = manager.Status(ctx)
	if !status.Installed || status.LastError == "" {
		t.Fatalf("PowerShell failure must preserve installed state and expose LastError: %+v", status)
	}

	runner.errors = map[string]error{queryKey: realExitError(t)}
	runner.responses[queryKey] = "ERROR: The system cannot find the file specified.\n"
	status = manager.Status(ctx)
	if status.Installed || status.Enabled || status.Active || status.LastError != "" {
		t.Fatalf("a missing task must produce a normal uninstalled result: %+v", status)
	}

	runner.errors = map[string]error{queryKey: errors.New(`exec: "schtasks.exe": executable file not found in %PATH%`)}
	runner.responses[queryKey] = ""
	status = manager.Status(ctx)
	if status.Installed || status.LastError == "" {
		t.Fatalf("unavailable schtasks must expose LastError: %+v", status)
	}
}

func TestSchtasksUninstall(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string]string{
			"schtasks.exe /End /TN NexTermAgent":       "",
			"schtasks.exe /Delete /TN NexTermAgent /F": "",
			"sc.exe stop NexTermAgent":                 "[SC] OpenService FAILED 1060:\n",
			"sc.exe delete NexTermAgent":               "[SC] OpenService FAILED 1060:\n",
		},
		errors: map[string]error{
			"schtasks.exe /End /TN NexTermAgent": realExitError(t),
			"sc.exe stop NexTermAgent":           realExitError(t),
			"sc.exe delete NexTermAgent":         realExitError(t),
		},
	}
	manager := newTestWindowsManager(runner)
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall = %v", err)
	}
	expectedOrder := []string{
		"schtasks.exe /End /TN NexTermAgent",
		"schtasks.exe /Delete /TN NexTermAgent /F",
		"sc.exe stop NexTermAgent",
		"sc.exe delete NexTermAgent",
	}
	if len(runner.commands) != len(expectedOrder) {
		t.Fatalf("uninstall commands = %+v", runner.commands)
	}
	for i, expected := range expectedOrder {
		got := runner.commands[i].name + " " + strings.Join(runner.commands[i].args, " ")
		if got != expected {
			t.Fatalf("uninstall step %d = %q, want %q", i, got, expected)
		}
	}

	deleteKey := "schtasks.exe /Delete /TN NexTermAgent /F"
	runner.errors[deleteKey] = realExitError(t)
	runner.responses[deleteKey] = "ERROR: The system cannot find the file specified.\n"
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall must succeed when the task does not exist: %v", err)
	}
	runner.responses[deleteKey] = "错误: 找不到指定的文件。\n"
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("localized missing-task output must also be tolerated: %v", err)
	}

	runner.responses[deleteKey] = "ERROR: Access is denied.\n"
	err := manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") {
		t.Fatalf("deletion denial must be actionable, got %v", err)
	}

	runner.responses[deleteKey] = ""
	runner.errors = map[string]error{"sc.exe delete NexTermAgent": realExitError(t)}
	runner.responses["sc.exe delete NexTermAgent"] = "[SC] OpenService FAILED 5:\n"
	err = manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("legacy system service cleanup failure must mention administrator privileges, got %v", err)
	}
}

func TestSchtasksUninstallReportsMissingTool(t *testing.T) {
	missing := errors.New(`exec: "schtasks.exe": executable file not found in %PATH%`)
	runner := &fakeRunner{
		errors: map[string]error{
			"schtasks.exe /End /TN NexTermAgent":       missing,
			"schtasks.exe /Delete /TN NexTermAgent /F": missing,
		},
	}
	err := newTestWindowsManager(runner).Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "schtasks.exe 不可用") {
		t.Fatalf("missing schtasks must be reported explicitly, got %v", err)
	}
}
