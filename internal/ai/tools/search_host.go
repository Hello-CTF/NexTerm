package tools

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	fsssh "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	searchPlatformPOSIX   = "posix"
	searchPlatformWindows = "windows"
	// searchPlatformNone means the host has no exec channel that runs a POSIX shell or PowerShell.
	searchPlatformNone = "none"
)

// searchHost is what a session's host can run for a search, learned once per transport generation.
type searchHost struct {
	generation uint64
	platform   string
	rg         bool
	findPrintf bool
	grepPCRE   bool
	grepNull   bool
	grepBinary bool
	grepPrune  bool
	grepFilter bool
}

const searchPOSIXSentinel = "posix"

// searchPOSIXProbe reports the shell and the search programs and options the host supports. Each grep option is
// tried on /dev/null, where a supported option exits 1 (no match) and an unknown one exits 2.
var searchPOSIXProbe = strings.Join([]string{
	"printf '%s\\n' '" + searchSentinel + " " + searchPOSIXSentinel + "'",
	"if command -v rg >/dev/null 2>&1; then printf '%s\\n' rg; fi",
	"if find . -maxdepth 0 -printf '' >/dev/null 2>&1; then printf '%s\\n' find-printf; fi",
	"grep -P -q x /dev/null 2>/dev/null; if [ $? -eq 1 ]; then printf '%s\\n' grep-pcre; fi",
	"grep --null -q x /dev/null 2>/dev/null; if [ $? -eq 1 ]; then printf '%s\\n' grep-null; fi",
	"grep -I -q x /dev/null 2>/dev/null; if [ $? -eq 1 ]; then printf '%s\\n' grep-binary; fi",
	"grep -r --exclude-dir=.git -q x /dev/null 2>/dev/null; if [ $? -eq 1 ]; then printf '%s\\n' grep-prune; fi",
	"grep -r --include='*' -q x /dev/null 2>/dev/null; if [ $? -eq 1 ]; then printf '%s\\n' grep-filter; fi",
}, "; ")

var searchPowerShellProbe = psquote.ShortCommand(searchPowerShellPrelude + "$o=[Console]::Out;$o.Write('" + searchSentinel + " powershell')")

// knownSearchPlatform is the platform a transport tells without probing; empty means it must be probed.
func knownSearchPlatform(transport base.Transport) string {
	switch transport.Kind() {
	case "winrm":
		return searchPlatformWindows
	case "telnet":
		return searchPlatformNone
	case "local":
		if runtime.GOOS == "windows" {
			return searchPlatformWindows
		}
	case "device":
		if device, ok := transport.(interface{ Platform() string }); ok && device.Platform() == "windows" {
			return searchPlatformWindows
		}
	}
	return ""
}

// searchHost learns, and caches per session and transport generation, how the host can be searched. A probe that
// fails for a transient reason is not cached.
func (r *Registry) searchHost(ctx context.Context, sessionID string, transport base.Transport) (searchHost, error) {
	generation := transport.Generation()
	if cached, ok := r.searchHosts.Load(sessionID); ok {
		if host := cached.(searchHost); host.generation == generation {
			return host, nil
		}
	}
	host := searchHost{generation: generation, platform: knownSearchPlatform(transport)}
	if host.platform == searchPlatformWindows || host.platform == searchPlatformNone {
		r.searchHosts.Store(sessionID, host)
		return host, nil
	}
	probeContext, cancel := context.WithTimeout(ctx, searchProbeTimeout)
	defer cancel()
	result, err := transport.Exec(probeContext, searchPOSIXProbe, base.ExecOptions{Timeout: searchProbeTimeout, Limits: base.OutputLimits{Stdout: 4 << 10, Stderr: 4 << 10}, ExpectedGeneration: generation})
	if ctx.Err() != nil {
		return searchHost{}, ctx.Err()
	}
	if errors.Is(err, base.ErrUnsupported) {
		host.platform = searchPlatformNone
		r.searchHosts.Store(sessionID, host)
		return host, nil
	}
	// Startup files of some hosts print before any command output, so the sentinel may not come first.
	if err == nil && strings.Contains(result.Stdout, searchSentinel+" "+searchPOSIXSentinel) {
		host.platform = searchPlatformPOSIX
		for _, line := range strings.Split(result.Stdout, "\n") {
			switch strings.TrimSpace(line) {
			case "rg":
				host.rg = true
			case "find-printf":
				host.findPrintf = true
			case "grep-pcre":
				host.grepPCRE = true
			case "grep-null":
				host.grepNull = true
			case "grep-binary":
				host.grepBinary = true
			case "grep-prune":
				host.grepPrune = true
			case "grep-filter":
				host.grepFilter = true
			}
		}
		r.searchHosts.Store(sessionID, host)
		return host, nil
	}
	if err != nil {
		// Not cached: the next call probes again.
		host.platform = searchPlatformNone
		return host, nil
	}
	if transport.Kind() == "local" || transport.Kind() == "device" {
		host.platform = searchPlatformNone
		r.searchHosts.Store(sessionID, host)
		return host, nil
	}
	// An SSH host whose exec shell is not POSIX may be Windows OpenSSH (cmd or PowerShell).
	result, err = transport.Exec(probeContext, searchPowerShellProbe, base.ExecOptions{Timeout: searchProbeTimeout, Limits: base.OutputLimits{Stdout: 4 << 10, Stderr: 4 << 10}, ExpectedGeneration: generation})
	if ctx.Err() != nil {
		return searchHost{}, ctx.Err()
	}
	if err != nil {
		host.platform = searchPlatformNone
		return host, nil
	}
	host.platform = searchPlatformNone
	if strings.Contains(result.Stdout, searchSentinel+" powershell") {
		host.platform = searchPlatformWindows
	}
	r.searchHosts.Store(sessionID, host)
	return host, nil
}

// posixPathWord is path as one single-quoted shell word, with the command that must run first. A path under "~"
// starts with a bare cd to the home directory and continues relative to it, so the script needs no expansion.
func posixPathWord(value string) (string, string) {
	switch {
	case value == "~":
		return "cd && ", "."
	case strings.HasPrefix(value, "~/"):
		return "cd && ", fsssh.ShellQuote(value[2:])
	}
	return "", fsssh.ShellQuote(value)
}

func quoteWords(words []string) string {
	quoted := make([]string, len(words))
	for index, word := range words {
		quoted[index] = fsssh.ShellQuote(word)
	}
	return strings.Join(quoted, " ")
}

// rgGlob escapes a leading "!", which rg would read as an exclusion.
func rgGlob(glob string) string {
	if strings.HasPrefix(glob, "!") {
		return `\` + glob
	}
	return glob
}

// buildPOSIXSearch returns the backend name, whether its output is NUL-separated, and the command. The script goes
// to the exec shell as is, like exec_commands and the host process view do, rather than wrapped in "sh -c": the
// guard reads a nested single-quoted script differently from the shell. A host whose exec shell is not POSIX (fish,
// cmd) fails the probe and is searched another way.
func buildPOSIXSearch(host searchHost, request searchRequest) (string, bool, string, *Output) {
	prefix, root := posixPathWord(request.path)
	head := "printf '%s\\n' '" + searchSentinel + " " + searchPOSIXSentinel + "'; " + prefix
	rootLine := func(backend string) string {
		return "printf '%s\\n' \"" + searchSentinel + " root $PWD\" '" + searchSentinel + " backend " + backend + "'"
	}
	if request.find {
		backend, words := posixFindWords(host, request)
		script := head + "if [ -d " + root + " ]; then cd -- " + root + " && " + rootLine(backend) + " && " + quoteWords(words) +
			"; else printf '%s\\n' '" + searchSentinel + " notdir'; fi"
		return backend, false, script, nil
	}
	if request.multiline && !host.rg {
		failure := searchFailure("multiline=true 需要主机上安装 ripgrep (rg)；该主机只能用 grep 搜索，请去掉 multiline 改用单行正则",
			"multiline=true needs ripgrep (rg) on the host, which this host does not have; drop multiline and use a single-line regex")
		return "", false, "", &failure
	}
	backend, nul, words := posixGrepWords(host, request)
	fileLine := "printf '%s\\n' '" + searchSentinel + " file' '" + searchSentinel + " backend " + backend + "'"
	script := head + "if [ -d " + root + " ]; then cd -- " + root + " && " + rootLine(backend) + " && " + quoteWords(words) + " ." +
		"; elif [ -e " + root + " ]; then " + fileLine + " && " + quoteWords(words) + " " + root +
		"; else printf '%s\\n' '" + searchSentinel + " missing'; fi"
	return backend, nul, script, nil
}

func posixFindWords(host searchHost, request searchRequest) (string, []string) {
	if host.rg {
		words := []string{"rg", "--files", "--no-config", "--hidden", "--sortr=modified"}
		if request.includeIgnored {
			words = append(words, "--no-ignore")
		}
		for _, glob := range request.names.expanded {
			words = append(words, "-g", rgGlob(glob))
		}
		if !request.includeIgnored {
			for _, skip := range searchSkipDirs {
				words = append(words, "-g", "!"+skip)
			}
		}
		return "ripgrep", append(words, "--", ".")
	}
	words := []string{"find", "."}
	if !request.includeIgnored {
		words = append(words, "(")
		for index, skip := range searchSkipDirs {
			if index > 0 {
				words = append(words, "-o")
			}
			words = append(words, "-name", skip)
		}
		words = append(words, ")", "-prune", "-o")
	}
	words = append(words, "-type", "f")
	if request.names.baseNameOnly() {
		words = append(words, "(")
		for index, glob := range request.names.expanded {
			if index > 0 {
				words = append(words, "-o")
			}
			words = append(words, "-name", glob)
		}
		words = append(words, ")")
	}
	if host.findPrintf {
		return "find", append(words, "-printf", `%T@\t%P\n`)
	}
	return "find", append(words, "-print")
}

// posixGrepWords builds an rg or grep command line ending in "--", to be followed by the search target.
func posixGrepWords(host searchHost, request searchRequest) (string, bool, []string) {
	if host.rg {
		words := []string{"rg", "--no-config", "--hidden", "--color", "never", "--null", "--no-heading", "--with-filename"}
		switch request.mode {
		case searchOutputFiles:
			words = append(words, "-l")
		case searchOutputCount:
			words = append(words, "-c")
		default:
			words = append(words, "-n", "--sort", "path")
			if request.context > 0 {
				words = append(words, "-C", strconv.Itoa(request.context))
			}
		}
		if request.ignoreCase {
			words = append(words, "-i")
		}
		if request.multiline {
			words = append(words, "-U", "--multiline-dotall")
		}
		if request.includeIgnored {
			words = append(words, "--no-ignore")
		}
		if request.fileType != "" {
			for _, glob := range searchFileTypes[request.fileType] {
				words = append(words, "--type-add", "nxsearch:"+glob)
			}
			words = append(words, "-t", "nxsearch")
		}
		for _, glob := range request.names.expanded {
			words = append(words, "-g", rgGlob(glob))
		}
		if !request.includeIgnored {
			for _, skip := range searchSkipDirs {
				words = append(words, "-g", "!"+skip)
			}
		}
		return "ripgrep", true, append(words, "-e", request.pattern, "--")
	}
	words := []string{"grep", "-r", "-H"}
	backend := "grep -E"
	if host.grepPCRE {
		words = append(words, "-P")
		backend = "grep -P"
	} else {
		words = append(words, "-E")
	}
	switch request.mode {
	case searchOutputFiles:
		words = append(words, "-l")
	case searchOutputCount:
		words = append(words, "-c")
	default:
		words = append(words, "-n")
		if request.context > 0 {
			words = append(words, "-C", strconv.Itoa(request.context))
		}
	}
	if request.ignoreCase {
		words = append(words, "-i")
	}
	if host.grepBinary {
		words = append(words, "-I")
	}
	if host.grepNull {
		words = append(words, "--null")
	}
	if host.grepPrune && !request.includeIgnored {
		for _, skip := range searchSkipDirs {
			words = append(words, "--exclude-dir="+skip)
		}
	}
	// --include narrows by base name only; finish applies the exact glob and type to what grep returns.
	if host.grepFilter {
		switch {
		case request.fileType != "":
			for _, glob := range searchFileTypes[request.fileType] {
				words = append(words, "--include="+glob)
			}
		case !request.names.empty() && request.names.baseNameOnly():
			for _, glob := range request.names.expanded {
				words = append(words, "--include="+glob)
			}
		}
	}
	return backend, host.grepNull, append(words, "-e", request.pattern, "--")
}

func runPOSIXSearch(ctx context.Context, transport base.Transport, host searchHost, request searchRequest) Output {
	backend, nul, command, failure := buildPOSIXSearch(host, request)
	if failure != nil {
		return *failure
	}
	// The command is built from quoted literals; the guard confirms it stays a read-only search, so no argument
	// can turn it into anything else.
	if ruling := guard.ClassifyCommand(command, nil); ruling.Risk != guard.Safe {
		return searchFailure("生成的搜索命令未通过只读检查，已拒绝执行: "+ruling.Reason, "the generated search command did not pass the read-only check and was not run: "+ruling.Reason)
	}
	result, timedOut, err := execSearch(ctx, transport, command)
	if err != nil {
		return Fail(err)
	}
	header, data := splitSearchOutput(result.Stdout, searchPOSIXSentinel)
	switch {
	case header.notDir:
		return searchFailure(fmt.Sprintf("path 不是目录或不存在: %s", request.path), fmt.Sprintf("path is not a directory or does not exist: %s", request.path))
	case header.missing:
		return searchFailure(fmt.Sprintf("path 不存在: %s", request.path), fmt.Sprintf("path does not exist: %s", request.path))
	case header.backend == "":
		detail := firstLine(result.Stderr)
		if timedOut {
			detail = fmt.Sprintf("timeout after %ds", int(searchExecTimeout/time.Second))
		}
		return searchFailure(fmt.Sprintf("无法在 %s 中搜索: %s", request.path, detail), fmt.Sprintf("cannot search in %s: %s", request.path, detail))
	}
	timed := request.find && backend == "find" && host.findPrintf
	records := parseSearchRecords(request, data, nul, timed, result.Truncated || timedOut)
	code := -1
	if result.ExitCode != nil {
		code = *result.ExitCode
	}
	// rg and grep exit 1 for no match; find exits 1 on any error.
	failedExit := code >= 2 || backend == "find" && code != 0
	if len(records) == 0 && failedExit && !timedOut && !pathErrorsOnly(result.Stderr) {
		detail := firstLine(result.Stderr)
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", code)
		}
		return searchFailure(fmt.Sprintf("%s 失败（%s）: %s", request.tool(), header.backend, detail), fmt.Sprintf("%s failed (%s): %s", request.tool(), header.backend, detail))
	}
	found := searchResult{backend: header.backend, root: header.root, file: header.file, records: records, newestFirst: request.find && header.backend == "ripgrep"}
	if header.file {
		found.root = request.path
	}
	found.incomplete, found.incompleteModel = searchLimitNotes(result.Truncated, timedOut)
	found.skipped, found.skippedModel = searchErrorNotes(result.Stderr)
	return request.finish(found, false)
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return clipSearchLine(line)
		}
	}
	return ""
}
