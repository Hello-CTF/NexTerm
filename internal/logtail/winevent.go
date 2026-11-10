package logtail

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

const (
	// WinEventInitialCount is how many of the newest records are printed before following.
	WinEventInitialCount = 200
	// WinEventBatchSize bounds the records printed per poll, so a burst never produces unbounded output.
	WinEventBatchSize = 200
	// WinEventPollSeconds is the pause between polls.
	WinEventPollSeconds = 2
	// WinEventMessageLimit truncates the message of one record, in characters.
	WinEventMessageLimit = 2000
	// WinEventAccessDeniedExit is the exit code of the script when the account may not read the channel.
	WinEventAccessDeniedExit = 5
)

// winEventChannel matches the channel names Windows uses (System, Application, Microsoft-Windows-X/Operational).
// It excludes quotes, wildcards and control characters, so the name is a literal in the script.
var winEventChannel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,127}$`)

// winEventScript prints the newest records oldest first, then polls for records with a higher RecordId. $log is
// defined by the caller. An unreadable channel prints "access denied" on stderr and exits with
// WinEventAccessDeniedExit.
const winEventScript = `$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}
function Show($e){
$m='';if($e.Message){$m=($e.Message -replace '\s+',' ').Trim()}
if($m.Length -gt ` + "%[1]d" + `){$m=$m.Substring(0,` + "%[1]d" + `)+'...'}
$lvl=$e.LevelDisplayName;if(-not $lvl){$lvl='Information'}
'{0} [{1}] {2} (id {3}, rec {4}): {5}' -f $e.TimeCreated.ToString('yyyy-MM-dd HH:mm:ss'),$lvl,$e.ProviderName,$e.Id,$e.RecordId,$m
}
function Fetch($args2){
try{@(Get-WinEvent @args2)}catch{
if($_.FullyQualifiedErrorId -like 'NoMatchingEventsFound*'){return @()}
if($_.Exception -is [UnauthorizedAccessException] -or $_.Exception.Message -match 'denied'){[Console]::Error.WriteLine('access denied: the account cannot read the event log '+$log+' (run as an administrator or join Event Log Readers)');exit ` + "%[2]d" + `}
[Console]::Error.WriteLine($_.Exception.Message);exit 1
}
}
$last=[int64]0
$init=@(Fetch @{LogName=$log;MaxEvents=` + "%[3]d" + `})
[array]::Reverse($init)
foreach($e in $init){Show $e;if([int64]$e.RecordId -gt $last){$last=[int64]$e.RecordId}}
while($true){
Start-Sleep -Seconds ` + "%[4]d" + `
foreach($e in @(Fetch @{LogName=$log;FilterXPath='*[System[EventRecordID>'+$last+']]';MaxEvents=` + "%[5]d" + `;Oldest=$true})){Show $e;if([int64]$e.RecordId -gt $last){$last=[int64]$e.RecordId}}
}`

func winEventScriptFor(channel string) string {
	body := fmt.Sprintf(winEventScript, WinEventMessageLimit, WinEventAccessDeniedExit, WinEventInitialCount, WinEventPollSeconds, WinEventBatchSize)
	return "$log=" + psquote.Quote(channel) + "\n" + body
}

func winEventCommand(channel string) (string, error) {
	if !winEventChannel.MatchString(channel) {
		return "", fmt.Errorf("%w: Windows event log name must be 1-128 letters, digits, space or . _ / -, got %s", ErrInvalid, strconv.Quote(channel))
	}
	return psquote.ShortCommand(winEventScriptFor(channel)), nil
}
