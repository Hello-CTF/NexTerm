package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

// Windows-only actions. Start, stop, restart and status are shared with systemd.
const (
	ActionSetAutomatic = "set-automatic"
	ActionSetManual    = "set-manual"
	ActionSetDisabled  = "set-disabled"
)

const windowsServicesSentinel = "@@nexterm-services windows"

// windowsScriptPrelude makes errors terminate the script with exit code 1 and keeps non-ASCII names intact.
const windowsScriptPrelude = "$ErrorActionPreference='Stop'\n" +
	"$ProgressPreference='SilentlyContinue'\n" +
	"try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}\n"

// windowsListScript prints the sentinel followed by every Win32_Service as one compact JSON array. Fields that
// can be null are cast so the JSON keeps one type per field.
const windowsListScript = windowsScriptPrelude +
	"Write-Output '" + windowsServicesSentinel + "'\n" +
	"$items=@(Get-CimInstance Win32_Service | ForEach-Object {[pscustomobject]@{name=[string]$_.Name;displayName=[string]$_.DisplayName;state=[string]$_.State;startMode=[string]$_.StartMode;delayed=[bool]$_.DelayedAutoStart;pid=[int]$_.ProcessId}})\n" +
	"ConvertTo-Json -InputObject $items -Compress"

// windowsNotFoundExit is the exit code of an action script whose service does not exist.
const windowsNotFoundExit = 3

// windowsServiceLookup binds $svc to the one service whose name equals $name. The comparison is exact
// (case-insensitive like Windows), so a name is never read as a wildcard pattern.
const windowsServiceLookup = "$svc=@(Get-Service | Where-Object {$_.Name -eq $name})\n" +
	"if($svc.Count -ne 1){[Console]::Error.WriteLine('service not found: '+$name);exit 3}\n" +
	"$svc=$svc[0]\n"

const windowsReportState = "Write-Output ('{0}: {1}' -f $svc.Name,(Get-Service -Name $svc.Name).Status)"

// WindowsListCommand lists the services of a Windows host. It runs under cmd.exe and PowerShell alike.
func WindowsListCommand() string {
	return psquote.ShortCommand(windowsListScript)
}

// WindowsActionCommand builds the command for one action on one Windows service.
func WindowsActionCommand(action, name string) (string, error) {
	if err := validateWindowsServiceName(name); err != nil {
		return "", err
	}
	script, err := windowsActionScript(action, name)
	if err != nil {
		return "", err
	}
	return psquote.ShortCommand(script), nil
}

func windowsActionScript(action, name string) (string, error) {
	var body string
	switch action {
	case ActionStatus:
		body = "Get-CimInstance Win32_Service | Where-Object {$_.Name -eq $svc.Name} | Format-List Name,DisplayName,State,StartMode,DelayedAutoStart,ProcessId,StartName,PathName,Description | Out-String -Width 4096"
	case ActionStart:
		body = "Start-Service -InputObject $svc\n" + windowsReportState
	case ActionStop:
		body = "Stop-Service -InputObject $svc\n" + windowsReportState
	case ActionRestart:
		body = "Restart-Service -InputObject $svc\n" + windowsReportState
	case ActionSetAutomatic, ActionSetManual, ActionSetDisabled:
		startup := map[string]string{ActionSetAutomatic: "Automatic", ActionSetManual: "Manual", ActionSetDisabled: "Disabled"}[action]
		body = "Set-Service -Name $svc.Name -StartupType " + startup + "\n" +
			"Write-Output ('{0}: {1}' -f $svc.Name,(Get-CimInstance Win32_Service | Where-Object {$_.Name -eq $svc.Name}).StartMode)"
	default:
		return "", fmt.Errorf("%w: unsupported Windows service action %q", ErrInvalid, action)
	}
	return windowsScriptPrelude + "$name=" + psquote.Quote(name) + "\n" + windowsServiceLookup + body, nil
}

// validateWindowsServiceName accepts what the Service Control Manager accepts as a key name, minus wildcard
// characters, which no installed service needs and which PowerShell cmdlets would expand.
func validateWindowsServiceName(name string) error {
	if err := validateUnit(name); err != nil {
		return err
	}
	if len(name) > 256 || strings.ContainsAny(name, "/\\*?[]`") {
		return fmt.Errorf("%w: Windows service name must be at most 256 bytes without / \\ * ? [ ] or `", ErrInvalid)
	}
	return nil
}

type windowsServiceRecord struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"`
	StartMode   string `json:"startMode"`
	Delayed     bool   `json:"delayed"`
	PID         int    `json:"pid"`
}

// ParseWindowsServices reads the output of WindowsListCommand. ok is false when the sentinel or the JSON
// array is missing, which means the command did not run as written.
func ParseWindowsServices(output string) ([]ServiceUnit, bool) {
	_, payload, found := strings.Cut(output, windowsServicesSentinel)
	if !found {
		return []ServiceUnit{}, false
	}
	payload = strings.TrimSpace(payload)
	var records []windowsServiceRecord
	if err := json.Unmarshal([]byte(payload), &records); err != nil {
		// PowerShell before 5.1 can unwrap a one-element -InputObject array.
		var single windowsServiceRecord
		if err := json.Unmarshal([]byte(payload), &single); err != nil || single.Name == "" {
			return []ServiceUnit{}, false
		}
		records = []windowsServiceRecord{single}
	}
	units := make([]ServiceUnit, 0, len(records))
	for _, record := range records {
		if record.Name == "" {
			continue
		}
		units = append(units, ServiceUnit{
			Name: record.Name, Active: record.State, Description: record.DisplayName,
			StartMode: windowsStartMode(record.StartMode, record.Delayed), PID: record.PID,
		})
	}
	return units, true
}

// windowsStartMode reports a delayed automatic start as its own mode, as services.msc does.
func windowsStartMode(mode string, delayed bool) string {
	if delayed && strings.EqualFold(mode, "Auto") {
		return "Delayed"
	}
	return mode
}

func windowsHostResult(result execbatch.Result, list bool) ServiceHostResult {
	host := ServiceHostResult{Result: result, Platform: PlatformWindows, Systemd: "unknown", Units: []ServiceUnit{}}
	if result.Status == execbatch.StatusSuccess {
		if !list {
			return host
		}
		units, ok := ParseWindowsServices(result.Stdout)
		if ok {
			host.Units = units
			return host
		}
		host.FailureKind = "output"
		host.Result.Error = "无法识别 Windows 服务列表的输出"
		return host
	}
	host.FailureKind = classifyWindowsFailure(result)
	return host
}

func classifyWindowsFailure(result execbatch.Result) string {
	text := strings.ToLower(strings.Join([]string{result.Error, result.Stderr, result.Stdout}, "\n"))
	switch {
	case result.Status == execbatch.StatusTransportError:
		return "transport"
	case result.Status == execbatch.StatusTimeout:
		return "timeout"
	case result.Status == execbatch.StatusCanceled:
		return "canceled"
	case result.ExitCode != nil && *result.ExitCode == windowsNotFoundExit && strings.Contains(text, "service not found"):
		return "not_found"
	case strings.Contains(text, "access is denied"), strings.Contains(text, "access denied"),
		strings.Contains(text, "permissiondenied"), strings.Contains(text, "unauthorizedaccess"),
		strings.Contains(text, "cannot open") && strings.Contains(text, "service"):
		return "permission"
	case strings.Contains(text, "powershell") && (strings.Contains(text, "not found") || strings.Contains(text, "not recognized")):
		return "powershell"
	default:
		return "exit"
	}
}
