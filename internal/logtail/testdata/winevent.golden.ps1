$log='System'
$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}
function Show($e){
$m='';if($e.Message){$m=($e.Message -replace '\s+',' ').Trim()}
if($m.Length -gt 2000){$m=$m.Substring(0,2000)+'...'}
$lvl=$e.LevelDisplayName;if(-not $lvl){$lvl='Information'}
'{0} [{1}] {2} (id {3}, rec {4}): {5}' -f $e.TimeCreated.ToString('yyyy-MM-dd HH:mm:ss'),$lvl,$e.ProviderName,$e.Id,$e.RecordId,$m
}
function Fetch($args2){
try{@(Get-WinEvent @args2)}catch{
if($_.FullyQualifiedErrorId -like 'NoMatchingEventsFound*'){return @()}
if($_.Exception -is [UnauthorizedAccessException] -or $_.Exception.Message -match 'denied'){[Console]::Error.WriteLine('access denied: the account cannot read the event log '+$log+' (run as an administrator or join Event Log Readers)');exit 5}
[Console]::Error.WriteLine($_.Exception.Message);exit 1
}
}
$last=[int64]0
$init=@(Fetch @{LogName=$log;MaxEvents=200})
[array]::Reverse($init)
foreach($e in $init){Show $e;if([int64]$e.RecordId -gt $last){$last=[int64]$e.RecordId}}
while($true){
Start-Sleep -Seconds 2
foreach($e in @(Fetch @{LogName=$log;FilterXPath='*[System[EventRecordID>'+$last+']]';MaxEvents=200;Oldest=$true})){Show $e;if([int64]$e.RecordId -gt $last){$last=[int64]$e.RecordId}}
}