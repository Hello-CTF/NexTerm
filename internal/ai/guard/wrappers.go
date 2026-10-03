package guard

import "strings"

// execWrapper describes a command that wraps another command's execution.
// Options are consumed with getopt semantics (attached or separate values,
// short clusters); the first remaining positional is the wrapped command.
// The registry only unwraps to the real command: an option the table cannot
// resolve makes the whole command Unknowable instead of being guessed away.
type execWrapper struct {
	valueShorts  string
	valueLongs   []string
	flagShorts   string
	flagLongs    []string
	skipArgs     int
	pidOption    bool
	stdinCommand bool
	stdinArgs    bool
}

var execWrappers = map[string]execWrapper{
	"nice":        {valueShorts: "n", valueLongs: []string{"adjustment"}},
	"nohup":       {},
	"timeout":     {valueShorts: "ks", valueLongs: []string{"kill-after", "signal"}, flagShorts: "v", flagLongs: []string{"preserve-status", "foreground", "verbose"}, skipArgs: 1},
	"stdbuf":      {valueShorts: "ioe", valueLongs: []string{"input", "output", "error"}},
	"command":     {flagShorts: "pvV"},
	"builtin":     {},
	"exec":        {valueShorts: "a", flagShorts: "cl"},
	"time":        {flagShorts: "p"},
	"watch":       {valueShorts: "n", valueLongs: []string{"interval"}, flagShorts: "bdeptx", flagLongs: []string{"beep", "color", "differences", "errexit", "chgexit", "precise", "no-title", "exec"}},
	"chrt":        {valueShorts: "p", valueLongs: []string{"pid"}, flagShorts: "frobFm", flagLongs: []string{"fifo", "rr", "other", "batch", "deadline", "reset-on-fork", "max"}, skipArgs: 1, pidOption: true},
	"setsid":      {flagShorts: "cwf", flagLongs: []string{"ctty", "wait", "fork"}},
	"ionice":      {valueShorts: "cnpu", valueLongs: []string{"class", "classdata", "uid", "pid"}, pidOption: true},
	"unshare":     {flagShorts: "f", valueLongs: []string{"map-user", "map-group", "setgroups"}, flagLongs: []string{"fork", "map-auto", "map-root-user", "mount-proc", "uts", "ipc", "net", "pid", "user", "cgroup", "time", "keep-caps"}},
	"nsenter":     {valueShorts: "t", valueLongs: []string{"target", "setuid", "setgid", "root", "wd"}, flagShorts: "amuipnCUT", flagLongs: []string{"all", "preserve-credentials", "no-fork", "mount", "uts", "ipc", "net", "pid", "cgroup", "user", "time"}},
	"systemd-run": {valueShorts: "pE", valueLongs: []string{"unit", "description", "slice", "property", "uid", "gid", "nice", "working-directory", "setenv", "on-calendar", "timer-property", "path-property"}, flagShorts: "q", flagLongs: []string{"scope", "user", "system", "quiet", "no-block", "wait", "collect", "send-sighup", "pipe", "interactive", "pty"}},
	"at":          {valueShorts: "fqt", flagShorts: "bmMlcvVrd", stdinCommand: true},
	"batch":       {flagShorts: "m", stdinCommand: true},
	"xargs":       {valueShorts: "adEIJLnsP", valueLongs: []string{"arg-file", "delimiter", "eof", "replace", "max-lines", "max-args", "max-chars", "max-procs", "process-slot-var"}, flagShorts: "0eilprtx", flagLongs: []string{"null", "no-run-if-empty", "interactive", "verbose", "exit", "show-limits"}, stdinArgs: true},
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// wrapperScan is the parsed leading-option state shared by every wrapper.
type wrapperScan struct {
	pidMode      bool
	replace      string
	replaceSplit bool
	fileInput    string
	listOnly     bool
	removeOnly   bool
}

func (s *wrapperScan) recordValue(key, value string) {
	switch key {
	case "replace", "I":
		s.replace = value
	case "J":
		// BSD -J: documented as line replacement, but real implementations
		// word-split the line and only replace standalone placeholders.
		// Both interpretations are classified.
		s.replace = value
		s.replaceSplit = true
	case "file", "f", "arg-file", "a":
		s.fileInput = value
	}
}

func (s *wrapperScan) recordFlag(key string) {
	switch key {
	case "l":
		s.listOnly = true
	case "r", "d":
		s.removeOnly = true
	}
}

type wrapperParse struct {
	scan   wrapperScan
	nested []string
	reason string
}

// parseWrapper consumes a wrapper's leading options with getopt semantics
// and returns the wrapped command argv. Anything it cannot resolve (unknown
// option, missing value) is reported through reason so callers fail closed.
func parseWrapper(name string, wrapper execWrapper, args []string) wrapperParse {
	index := 0
	var scan wrapperScan
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if strings.HasPrefix(arg, "--") {
			long, value, attached := strings.Cut(arg[2:], "=")
			if containsString(wrapper.valueLongs, long) {
				if wrapper.pidOption && long == "pid" {
					scan.pidMode = true
				}
				if !attached {
					index++
					value = ""
					if index < len(args) {
						value = args[index]
					}
				}
				scan.recordValue(long, value)
				index++
				continue
			}
			if containsString(wrapper.flagLongs, long) {
				scan.recordFlag(long)
				index++
				continue
			}
			return wrapperParse{reason: name + " 包含无法识别的长选项"}
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			letters := arg[1:]
			for len(letters) > 0 {
				letter := letters[0]
				if strings.IndexByte(wrapper.valueShorts, letter) >= 0 {
					if wrapper.pidOption && letter == 'p' {
						scan.pidMode = true
					}
					value := letters[1:]
					if value == "" {
						index++
						if index < len(args) {
							value = args[index]
						}
					}
					scan.recordValue(string(letter), value)
					letters = ""
					continue
				}
				if strings.IndexByte(wrapper.flagShorts, letter) >= 0 {
					scan.recordFlag(string(letter))
					letters = letters[1:]
					continue
				}
				return wrapperParse{reason: name + " 包含无法识别的选项"}
			}
			index++
			continue
		}
		break
	}
	for skip := 0; skip < wrapper.skipArgs && index < len(args); skip++ {
		index++
	}
	if index > len(args) {
		index = len(args)
	}
	return wrapperParse{scan: scan, nested: args[index:]}
}

func (c *classifier) classifyWrapper(name string, wrapper execWrapper, args []string, stdin pipeInput) Ruling {
	parsed := parseWrapper(name, wrapper, args)
	if parsed.reason != "" {
		return Indeterminate(parsed.reason)
	}
	scan := parsed.scan
	if wrapper.stdinCommand {
		// at -f executes the file's content, which cannot be verified; it
		// takes precedence over anything read from standard input.
		if scan.fileInput != "" {
			return Dangerous(name + " 执行文件中的命令，内容无法验证")
		}
		if scan.listOnly {
			return Allow()
		}
		if scan.removeOnly {
			return Confirm(KindService, name+" 删除计划任务")
		}
		if stdin.kind == stdinPipe || stdin.kind == stdinHeredoc {
			if !stdin.ok {
				return Dangerous("管道输入无法完整确定，执行器可能运行未审查的内容")
			}
			// Installing a scheduled job always confirms at minimum; the
			// payload itself is classified with the full model.
			return Worst(Confirm(KindService, name+" 安装计划任务"), c.classifyText(stdin.text))
		}
		return Dangerous(name + " 执行标准输入中的命令")
	}
	if wrapper.pidOption && scan.pidMode {
		return Confirm(KindProcess, "进程调度参数变更")
	}
	if len(parsed.nested) == 0 {
		if wrapper.stdinArgs {
			// xargs with no command runs its built-in echo: reading stdin
			// and printing it is the narrow read-only default.
			return Allow()
		}
		if name == "exec" {
			// exec with only redirections rearranges file descriptors.
			return Allow()
		}
		return Indeterminate(name + " 缺少要执行的命令")
	}
	if wrapper.stdinArgs {
		return c.classifyXargs(parsed.nested, scan, stdin)
	}
	return c.child().classifyArgv(parsed.nested, stdin)
}

// classifyXargs models xargs semantics: with -I/-J/--replace every occurrence
// of the replacement string in the initial arguments is substituted with one
// input line per invocation; without it, input words are appended to the
// command. Unknown or non-determinable input fails closed to Danger.
func (c *classifier) classifyXargs(nested []string, scan wrapperScan, stdin pipeInput) Ruling {
	if scan.fileInput != "" {
		return Dangerous("xargs 从文件构建命令")
	}
	if stdin.kind == stdinFile {
		return Dangerous("xargs 从文件构建命令")
	}
	if stdin.kind != stdinPipe && stdin.kind != stdinHeredoc || !stdin.ok || strings.TrimSpace(stdin.text) == "" {
		if recursiveDeleteCommand(nested) {
			return Dangerous("xargs 以未受控输入调用递归删除")
		}
		return Dangerous("xargs 从标准输入构建命令")
	}
	result := Allow()
	lines := strings.Split(stdin.text, "\n")
	classified := 0
	for _, line := range lines {
		if scan.replace != "" {
			if line == "" {
				continue
			}
			classified++
			result = Worst(result, c.classifyXargsLine(nested, scan, line))
			continue
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		classified++
		combined := make([]string, 0, len(nested)+len(words))
		combined = append(combined, nested...)
		combined = append(combined, words...)
		result = Worst(result, c.child().classifyArgv(combined, pipeInput{}))
	}
	if classified == 0 {
		return Dangerous("xargs 从标准输入构建命令")
	}
	return result
}

// classifyXargsLine classifies one input line under replacement semantics.
// GNU -I replaces every placeholder occurrence with the whole line as one
// argument; BSD -J replaces only standalone placeholder tokens with the
// line's words. Because implementations disagree, both interpretations are
// classified and the worse ruling wins.
func (c *classifier) classifyXargsLine(nested []string, scan wrapperScan, line string) Ruling {
	substituted := make([]string, len(nested))
	for i, token := range nested {
		substituted[i] = strings.ReplaceAll(token, scan.replace, line)
	}
	result := c.child().classifyArgv(substituted, pipeInput{})
	if scan.replaceSplit {
		words := strings.Fields(line)
		combined := make([]string, 0, len(nested)+len(words))
		replaced := false
		for _, token := range nested {
			if token == scan.replace {
				combined = append(combined, words...)
				replaced = true
				continue
			}
			combined = append(combined, token)
		}
		if !replaced {
			combined = append(combined, words...)
		}
		result = Worst(result, c.child().classifyArgv(combined, pipeInput{}))
	}
	return result
}

func recursiveDeleteCommand(tokens []string) bool {
	switch commandName(tokens[0]) {
	case "rm", "rmdir", "shred":
	default:
		return false
	}
	for _, arg := range tokens[1:] {
		if strings.HasPrefix(arg, "-") && (strings.ContainsRune(arg, 'r') || strings.ContainsRune(arg, 'R')) {
			return true
		}
	}
	return false
}
