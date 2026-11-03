package hostprocs

import (
	"encoding/base64"
	"strconv"
	"unicode/utf16"
)

const sentinelPrefix = "@@nexterm-procs "

// listScript prints a sentinel naming the ps flavour that worked, followed by its output. "-A" rather than
// "-e" because macOS reads "-e" as show-environment; sorting is always done locally.
const listScript = "if out=$(LC_ALL=C ps -A -o pid,ppid,user,%cpu,%mem,rss,etime,stat,args 2>/dev/null); then printf '%s\\n%s\\n' '" + sentinelPrefix + "ps' \"$out\"\n" +
	"elif out=$(LC_ALL=C ps -o pid,ppid,user,rss,etime,stat,args 2>/dev/null); then printf '%s\\n%s\\n' '" + sentinelPrefix + "ps-busybox' \"$out\"\n" +
	"else printf '%s\\n' '" + sentinelPrefix + "none'\n" +
	"fi"

const posixPlatformProbe = "printf '%s\\n' '" + sentinelPrefix + "posix'"

// windowsScript lists processes as one JSON object; it runs through -EncodedCommand so neither cmd nor
// PowerShell needs any escaping.
const windowsScript = `$ErrorActionPreference='SilentlyContinue'
try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}
Write-Output '` + sentinelPrefix + `windows-cim'
$cores=[Environment]::ProcessorCount
$total=[double](Get-CimInstance Win32_OperatingSystem).TotalVisibleMemorySize
$now=Get-Date
$cpuOk=$false;$cpu=@{}
$perf=@(Get-CimInstance Win32_PerfFormattedData_PerfProc_Process)
if($perf.Count -gt 0){$cpuOk=$true;foreach($p in $perf){$cpu[[int]$p.IDProcess]=[double]$p.PercentProcessorTime}}
$userOk=$false;$users=@{}
try{foreach($p in @(Get-Process -IncludeUserName -ErrorAction Stop)){$users[[int]$p.Id]=$p.UserName};$userOk=$true}catch{}
$items=@(foreach($p in @(Get-CimInstance Win32_Process)){
$id=[int]$p.ProcessId
$c=0.0;if($cpu.ContainsKey($id)){$c=[math]::Round($cpu[$id]/$cores,1)}
$el=-1;if($p.CreationDate){$el=[int64]($now-$p.CreationDate).TotalSeconds}
[pscustomobject]@{pid=$id;ppid=[int]$p.ParentProcessId;name=$p.Name;cmd=$p.CommandLine;rssKiB=[int64]($p.WorkingSetSize/1024);cpu=$c;user=$users[$id];elapsedSec=$el}
})
[pscustomobject]@{cpuOk=$cpuOk;userOk=$userOk;memTotalKiB=$total;items=$items}|ConvertTo-Json -Compress -Depth 4`

func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(units)*2)
	for _, u := range units {
		raw = append(raw, byte(u), byte(u>>8))
	}
	return "powershell -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
}

var windowsListCommand = encodePowerShell(windowsScript)

func killCommand(platform string, pid int, force bool) (command, signal string) {
	id := strconv.Itoa(pid)
	if platform == platformWindows {
		if force {
			return "taskkill /PID " + id + " /F", "KILL"
		}
		return "taskkill /PID " + id, "TERM"
	}
	if force {
		return "kill -KILL " + id, "KILL"
	}
	return "kill -TERM " + id, "TERM"
}

// identityCommand prints what PID currently runs. The POSIX form falls back to a plain "ps" table for
// busybox, which has no "-p".
func identityCommand(platform string, pid int) string {
	id := strconv.Itoa(pid)
	if platform == platformWindows {
		return `tasklist /FI "PID eq ` + id + `" /FO CSV /NH`
	}
	return "ps -p " + id + " -o args= 2>/dev/null || ps -o pid,args 2>/dev/null | while read -r p a; do [ \"$p\" = " + id + " ] && printf '%s\\n' \"$a\"; done"
}
