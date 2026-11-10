package shellintegr

import "strings"

// PowerShellIntegrationFile is the PowerShell hooks file written into the wrapper directory.
const PowerShellIntegrationFile = "nexterm.ps1"

// PowerShellArgs returns the arguments that start an interactive PowerShell with the hooks in path. The user's
// profiles still load: -Command runs after them, and -NoExit keeps the session interactive. The file is read and
// dot-sourced as a script block rather than run as a script, so a Restricted or AllSigned execution policy
// neither blocks the integration nor changes which of the user's own scripts may run. A failure inside the
// hooks is swallowed so the shell always starts.
func PowerShellArgs(path string) []string {
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	return []string{"-NoLogo", "-NoExit", "-Command",
		"try { . ([scriptblock]::Create([System.IO.File]::ReadAllText(" + quoted + "))) } catch { }"}
}

// powerShellIntegration wraps the prompt function to report the finished command (OSC 133 D with its exit code),
// the prompt start (OSC 133 A) and the working directory (OSC 7, file://host/C:/path), then returns the user's
// own prompt unchanged with $? restored for it. When PSReadLine is loaded, the accepted command line is marked
// with OSC 133 B and C by wrapping PSConsoleHostReadLine, which leaves the user's key bindings alone; without
// PSReadLine only the prompt and exit reports are written.
const powerShellIntegration = `# nexterm shell integration
if (Test-Path Variable:global:__NexTermIntegration) { return }
$global:__NexTermIntegration = $true
$global:__NexTermOriginalPrompt = $function:prompt
$global:__NexTermLastHistoryId = -1
$global:__NexTermHost = $env:COMPUTERNAME
try { $global:__NexTermHost = [System.Net.Dns]::GetHostName() } catch { }

function global:__NexTermOSC7 {
    $location = $ExecutionContext.SessionState.Path.CurrentLocation
    if ($location.Provider.Name -ne 'FileSystem') { return '' }
    $path = $location.ProviderPath
    if ($path.StartsWith('\\')) { return '' }
    $encoded = @($path.Replace('\', '/').Split('/') | ForEach-Object { [System.Uri]::EscapeDataString($_) }) -join '/'
    if (-not $encoded.StartsWith('/')) { $encoded = '/' + $encoded }
    return "$([char]27)]7;file://$($global:__NexTermHost)$encoded$([char]27)\"
}

function global:prompt {
    $nexTermSuccess = $global:?
    $nexTermExitCode = $global:LASTEXITCODE
    Set-StrictMode -Off
    $report = "$([char]27)]133;D$([char]27)\"
    $entry = Get-History -Count 1
    if ($null -ne $entry -and $entry.Id -ne $global:__NexTermLastHistoryId) {
        $global:__NexTermLastHistoryId = $entry.Id
        $code = 0
        if (-not $nexTermSuccess) {
            $code = 1
            if ($nexTermExitCode -is [int] -and $nexTermExitCode -ne 0) {
                $code = [BitConverter]::ToUInt32([BitConverter]::GetBytes([int]$nexTermExitCode), 0)
            }
        }
        $report = "$([char]27)]133;D;$code$([char]27)\"
    }
    try {
        [Console]::Write($report + "$([char]27)]133;A$([char]27)\" + (__NexTermOSC7))
    } catch { }
    if (-not $nexTermSuccess) { Write-Error 'nexterm' -ErrorAction Ignore }
    if ($null -eq $global:__NexTermOriginalPrompt) {
        return "PS $($ExecutionContext.SessionState.Path.CurrentLocation)$('>' * ($NestedPromptLevel + 1)) "
    }
    return (@(& $global:__NexTermOriginalPrompt) -join '')
}

if (Test-Path Function:\PSConsoleHostReadLine) {
    $global:__NexTermOriginalReadLine = $function:PSConsoleHostReadLine
    function global:PSConsoleHostReadLine {
        $nexTermSuccess = $global:?
        if (-not $nexTermSuccess) { Write-Error 'nexterm' -ErrorAction Ignore }
        $line = & $global:__NexTermOriginalReadLine
        if (-not [string]::IsNullOrWhiteSpace($line)) {
            try { [Console]::Write("$([char]27)]133;B$([char]27)\$([char]27)]133;C$([char]27)\") } catch { }
        }
        $line
    }
}
`
