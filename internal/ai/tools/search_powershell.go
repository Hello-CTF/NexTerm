package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// searchPowerShellPrelude keeps progress records off stderr and non-ASCII output intact.
const searchPowerShellPrelude = "$ProgressPreference='SilentlyContinue';try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}\n"

// searchPowerShellBody walks the tree with .NET directory enumeration, so skipped directories are never entered,
// and prints records in the NUL-separated layout ripgrep uses: "path\0N:text" for a match, "path\0N-text" for
// context, "path\0N" for a count, and "mtime\tpath" for find_files. Its inputs are the variables set before it,
// all single-quoted literals: $P path, $M mode, $G path regex, $T type regex, $X pattern, $I ignore case, $C context
// lines, $S skipped directory names.
const searchPowerShellBody = `$o=[Console]::Out;$w=[Console]::Error;$o.Write("@@nexterm-search powershell` + "`n" + `")
$i=Get-Item -LiteralPath $P -Force -ErrorAction SilentlyContinue
if(-not $i){$o.Write("@@nexterm-search missing` + "`n" + `");return}
if($M -ne 'find'){try{$op=[Text.RegularExpressions.RegexOptions]::CultureInvariant;if($I){$op=$op -bor [Text.RegularExpressions.RegexOptions]::IgnoreCase};$R=New-Object Text.RegularExpressions.Regex($X,$op)}catch{$o.Write("@@nexterm-search failure "+$_.Exception.Message.Replace("` + "`n" + `"," "));return}}
function V($f,$n){
if($M -eq 'find'){$o.Write([string][long](($f.LastWriteTimeUtc-[datetime]'1970-01-01').TotalSeconds)+"` + "`t" + `"+$n+"` + "`n" + `");return}
try{$s=$f.OpenRead();$b=New-Object byte[] 8000;$k=$s.Read($b,0,8000);$s.Close()}catch{$w.WriteLine("${n}: "+$_.Exception.Message);return}
if([Array]::IndexOf($b,[byte]0,0,$k) -ge 0){return}
$l=0;$c=0;$a=0;$q=New-Object Collections.Generic.Queue[object]
try{foreach($t in [IO.File]::ReadLines($f.FullName)){$l++
if($R.IsMatch($t)){$c++;if($M -eq 'files'){break}
if($M -eq 'content'){while($q.Count){$x=$q.Dequeue();$o.Write($n+"` + "`0" + `"+$x[0]+"-"+$x[1]+"` + "`n" + `")};$o.Write($n+"` + "`0" + `"+$l+":"+$t+"` + "`n" + `");$a=$C}}
elseif($M -eq 'content'){if($a -gt 0){$a--;$o.Write($n+"` + "`0" + `"+$l+"-"+$t+"` + "`n" + `")}elseif($C -gt 0){$q.Enqueue(@($l,$t));if($q.Count -gt $C){[void]$q.Dequeue()}}}}}catch{$w.WriteLine("${n}: "+$_.Exception.Message);return}
if($c){if($M -eq 'files'){$o.Write($n+"` + "`n" + `")}elseif($M -eq 'count'){$o.Write($n+"` + "`0" + `"+$c+"` + "`n" + `")}}}
if(-not $i.PSIsContainer){if($M -eq 'find'){$o.Write("@@nexterm-search notdir` + "`n" + `");return};$o.Write("@@nexterm-search file` + "`n" + `@@nexterm-search backend PowerShell` + "`n" + `");V $i $i.Name;return}
$r=$i.FullName.TrimEnd('\','/');$o.Write("@@nexterm-search root "+$i.FullName+"` + "`n" + `@@nexterm-search backend PowerShell` + "`n" + `")
$d=New-Object Collections.Generic.Stack[string];$d.Push($i.FullName)
while($d.Count){$e=$d.Pop();try{$z=(New-Object IO.DirectoryInfo $e).GetFileSystemInfos()}catch{$w.WriteLine("${e}: "+$_.Exception.Message);continue}
foreach($f in $z){$n=$f.FullName.Substring($r.Length).TrimStart('\','/').Replace('\','/')
if($f -is [IO.DirectoryInfo]){if(($f.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $S -contains $f.Name){continue};$d.Push($f.FullName);continue}
if($G -and $n -notmatch $G){continue};if($T -and $f.Name -notmatch $T){continue};V $f $n}}`

func powerShellSearchScript(request searchRequest) (string, *Output) {
	if request.multiline {
		failure := searchFailure("multiline=true 需要主机上安装 ripgrep (rg)；Windows 主机用 PowerShell 搜索，不支持跨行匹配，请去掉 multiline 改用单行正则",
			"multiline=true needs ripgrep (rg); Windows hosts are searched with PowerShell, which cannot match across lines, so drop multiline and use a single-line regex")
		return "", &failure
	}
	mode := "find"
	if !request.find {
		mode = map[string]string{searchOutputFiles: "files", searchOutputCount: "count", searchOutputContent: "content"}[request.mode]
	}
	root := psquote.Quote(request.path)
	switch {
	case request.path == "~":
		root = "$HOME"
	case strings.HasPrefix(request.path, "~/") || strings.HasPrefix(request.path, `~\`):
		root = "(Join-Path $HOME " + psquote.Quote(request.path[2:]) + ")"
	}
	names, types := "''", "''"
	if !request.names.empty() {
		names = psquote.Quote(request.names.globRegexp())
	}
	if !request.types.empty() {
		types = psquote.Quote(request.types.globRegexp())
	}
	ignore := "$false"
	if request.ignoreCase {
		ignore = "$true"
	}
	skipped := "@()"
	if !request.includeIgnored {
		quoted := make([]string, len(searchSkipDirs))
		for index, name := range searchSkipDirs {
			quoted[index] = psquote.Quote(name)
		}
		skipped = "@(" + strings.Join(quoted, ",") + ")"
	}
	variables := "$P=" + root + ";$M=" + psquote.Quote(mode) + ";$G=" + names + ";$T=" + types + ";$X=" + psquote.Quote(request.pattern) +
		";$I=" + ignore + ";$C=" + strconv.Itoa(request.context) + ";$S=" + skipped + "\n"
	return psquote.ShortCommand(searchPowerShellPrelude + variables + searchPowerShellBody), nil
}

func runPowerShellSearch(ctx context.Context, transport base.Transport, request searchRequest) Output {
	command, failure := powerShellSearchScript(request)
	if failure != nil {
		return *failure
	}
	result, timedOut, err := execSearch(ctx, transport, command)
	if err != nil {
		return Fail(err)
	}
	header, data := splitSearchOutput(result.Stdout, "powershell")
	switch {
	case header.notDir:
		return searchFailure(fmt.Sprintf("path 不是目录: %s", request.path), fmt.Sprintf("path is not a directory: %s", request.path))
	case header.missing:
		return searchFailure(fmt.Sprintf("path 不存在: %s", request.path), fmt.Sprintf("path does not exist: %s", request.path))
	case header.failure != "":
		return searchFailure(fmt.Sprintf("内容正则无效: %s", header.failure), fmt.Sprintf("invalid content regex: %s", header.failure))
	case header.backend == "":
		detail := firstLine(result.Stderr)
		if timedOut {
			detail = fmt.Sprintf("timeout after %ds", int(searchExecTimeout/time.Second))
		}
		if detail == "" {
			detail = "PowerShell did not run the search"
		}
		return searchFailure(fmt.Sprintf("无法在 %s 中搜索: %s", request.path, detail), fmt.Sprintf("cannot search in %s: %s", request.path, detail))
	}
	records := parseSearchRecords(request, data, true, true, result.Truncated || timedOut)
	found := searchResult{backend: header.backend, root: header.root, file: header.file, records: records}
	if header.file {
		found.root = request.path
	}
	found.incomplete, found.incompleteModel = searchLimitNotes(result.Truncated, timedOut)
	found.skipped, found.skippedModel = searchErrorNotes(result.Stderr)
	return request.finish(found, true)
}
