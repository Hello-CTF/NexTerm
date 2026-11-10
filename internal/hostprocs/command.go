package hostprocs

import (
	"strconv"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

const sentinelPrefix = "@@nexterm-procs "

// posixLocale makes ps print lstart in a fixed layout and keeps multibyte names intact. Without that locale
// both the listing and the recheck fall back to C together, so they still agree with each other.
const posixLocale = "LC_ALL=C.UTF-8 "

const (
	sentinelComm       = sentinelPrefix + "comm"
	sentinelIDPS       = sentinelPrefix + "id-ps"
	sentinelIDArgs     = sentinelPrefix + "id-args"
	sentinelIDBusybox  = sentinelPrefix + "id-busybox"
	sentinelIDWindows  = sentinelPrefix + "id-windows"
	psFormatFull       = "ps"
	psFormatBusybox    = "ps-busybox"
	psFormatBusyboxMin = "ps-busybox-min"
)

// listScript prints a sentinel naming the ps flavour that worked, followed by its output. "-A" rather than
// "-e" because macOS reads "-e" as show-environment; sorting is always done locally. The full flavour is
// followed by a second table of pid and comm.
const listScript = "if out=$(" + posixLocale + "ps -A -o pid,ppid,user,%cpu,%mem,rss,etime,stat,lstart,args 2>/dev/null); then\n" +
	"comm=$(" + posixLocale + "ps -A -o pid=,comm= 2>/dev/null)\n" +
	"printf '%s\\n%s\\n%s\\n%s\\n' '" + sentinelPrefix + psFormatFull + "' \"$out\" '" + sentinelComm + "' \"$comm\"\n" +
	"elif out=$(" + posixLocale + "ps -o pid,ppid,user,rss,etime,stat,args 2>/dev/null); then printf '%s\\n%s\\n' '" + sentinelPrefix + psFormatBusybox + "' \"$out\"\n" +
	"elif out=$(" + posixLocale + "ps -o pid,ppid,user,rss,stat,args 2>/dev/null); then printf '%s\\n%s\\n' '" + sentinelPrefix + psFormatBusyboxMin + "' \"$out\"\n" +
	"else printf '%s\\n' '" + sentinelPrefix + "none'\n" +
	"fi"

const posixPlatformProbe = "printf '%s\\n' '" + sentinelPrefix + "posix'"

// windowsStartedExpr renders CreationDate as ISO-8601 UTC; the listing and the recheck must use the same form.
const windowsStartedExpr = `$(if($p.CreationDate){$p.CreationDate.ToUniversalTime().ToString('o')}else{''})`

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
if($perf.Count -gt 0){$cpuOk=$true;foreach($p in $perf){if($p.Name -eq '_Total' -or $p.Name -eq 'Idle'){continue};$cpu[[int]$p.IDProcess]=[double]$p.PercentProcessorTime}}
$userOk=$false;$users=@{}
try{foreach($p in @(Get-Process -IncludeUserName -ErrorAction Stop)){$users[[int]$p.Id]=$p.UserName};$userOk=$true}catch{}
$items=@(foreach($p in @(Get-CimInstance Win32_Process)){
$id=[int]$p.ProcessId
$c=0.0;if($cpu.ContainsKey($id)){$c=[math]::Round($cpu[$id]/$cores,1)}
$el=-1;if($p.CreationDate){$el=[int64]($now-$p.CreationDate).TotalSeconds}
[pscustomobject]@{pid=$id;ppid=[int]$p.ParentProcessId;name=$p.Name;cmd=$p.CommandLine;rssKiB=[int64]($p.WorkingSetSize/1024);cpu=$c;user=$users[$id];elapsedSec=$el;started=` + windowsStartedExpr + `}
})
[pscustomobject]@{cpuOk=$cpuOk;userOk=$userOk;memTotalKiB=$total;items=$items}|ConvertTo-Json -Compress -Depth 4`

func encodePowerShell(script string) string {
	return psquote.ShortCommand(script)
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

// identityCommand prints what PID currently runs: name, parent and start time. The POSIX form falls back to
// a plain "ps" table for busybox, which has no "-p" and no lstart.
func identityCommand(platform string, pid int) string {
	id := strconv.Itoa(pid)
	if platform == platformWindows {
		return encodePowerShell(`$ErrorActionPreference='SilentlyContinue'
try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}
Write-Output '` + sentinelIDWindows + `'
$items=@(foreach($p in @(Get-CimInstance Win32_Process -Filter "ProcessId=` + id + `")){[pscustomobject]@{name=$p.Name;ppid=[int]$p.ParentProcessId;started=` + windowsStartedExpr + `}})
ConvertTo-Json -InputObject $items -Compress`)
	}
	return "if out=$(" + posixLocale + "ps -p " + id + " -o ppid=,lstart= 2>/dev/null) && [ -n \"$out\" ]; then\n" +
		"args=$(" + posixLocale + "ps -p " + id + " -o args= 2>/dev/null)\n" +
		"printf '%s\\n%s\\n%s\\n%s\\n' '" + sentinelIDPS + "' \"$out\" '" + sentinelIDArgs + "' \"$args\"\n" +
		"else\n" +
		posixLocale + "ps -o pid,ppid,args 2>/dev/null | while read -r p pp a; do [ \"$p\" = " + id + " ] && { printf '%s\\n%s\\n%s\\n' '" + sentinelIDBusybox + "' \"$pp\" \"$a\"; break; }; done\n" +
		"fi"
}
