package guard

import "strings"

// execWrapper describes a command that wraps another command's execution.
// Options are consumed with getopt semantics (attached or separate values,
// short clusters); the first remaining positional is the wrapped command.
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
	"time":        {flagShorts: "p"},
	"watch":       {valueShorts: "n", valueLongs: []string{"interval"}, flagShorts: "bdeptx", flagLongs: []string{"beep", "color", "differences", "errexit", "chgexit", "precise", "no-title", "exec"}},
	"chrt":        {valueShorts: "p", valueLongs: []string{"pid"}, flagShorts: "frobFm", flagLongs: []string{"fifo", "rr", "other", "batch", "deadline", "reset-on-fork", "max"}, skipArgs: 1, pidOption: true},
	"setsid":      {flagShorts: "cwf", flagLongs: []string{"ctty", "wait", "fork"}},
	"ionice":      {valueShorts: "cnpu", valueLongs: []string{"class", "classdata", "uid", "pid"}, pidOption: true},
	"unshare":     {flagShorts: "f", valueLongs: []string{"map-user", "map-group", "setgroups"}, flagLongs: []string{"fork", "map-auto", "map-root-user", "mount-proc", "uts", "ipc", "net", "pid", "user", "cgroup", "time", "keep-caps"}},
	"nsenter":     {valueShorts: "t", valueLongs: []string{"target", "setuid", "setgid", "root", "wd"}, flagShorts: "amuipnCUT", flagLongs: []string{"all", "preserve-credentials", "no-fork", "mount", "uts", "ipc", "net", "pid", "cgroup", "user", "time"}},
	"systemd-run": {valueShorts: "pE", valueLongs: []string{"unit", "description", "slice", "property", "uid", "gid", "nice", "working-directory", "setenv", "on-calendar", "timer-property", "path-property"}, flagShorts: "q", flagLongs: []string{"scope", "user", "system", "quiet", "no-block", "wait", "collect", "send-sighup", "pipe", "interactive", "pty"}},
	"at":          {valueShorts: "fqrtd", flagShorts: "bmMlcV", stdinCommand: true},
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

func classifyWrapper(name string, wrapper execWrapper, args []string, rules []string, depth int, stdin stdinHint) Ruling {
	if wrapper.stdinCommand {
		if stdin.ok && strings.TrimSpace(stdin.text) != "" {
			return classifyCommandDepth(stdin.text, rules, depth+1)
		}
		return Dangerous(name + " 执行标准输入中的命令")
	}
	index := 0
	pidMode := false
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if strings.HasPrefix(arg, "--") {
			long, _, attached := strings.Cut(arg[2:], "=")
			if containsString(wrapper.valueLongs, long) {
				if wrapper.pidOption && long == "pid" {
					pidMode = true
				}
				if !attached {
					index++
				}
				index++
				continue
			}
			if containsString(wrapper.flagLongs, long) {
				index++
				continue
			}
			return Dangerous(name + " 包含无法识别的长选项")
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			letters := arg[1:]
			for len(letters) > 0 {
				letter := letters[0]
				if strings.IndexByte(wrapper.valueShorts, letter) >= 0 {
					if wrapper.pidOption && letter == 'p' {
						pidMode = true
					}
					if len(letters) == 1 {
						index++
					}
					letters = ""
					continue
				}
				if strings.IndexByte(wrapper.flagShorts, letter) >= 0 {
					letters = letters[1:]
					continue
				}
				return Dangerous(name + " 包含无法识别的选项")
			}
			index++
			continue
		}
		break
	}
	if wrapper.pidOption && pidMode {
		return Confirm(KindProcess, "进程调度参数变更")
	}
	for skip := 0; skip < wrapper.skipArgs && index < len(args); skip++ {
		index++
	}
	if index >= len(args) {
		if wrapper.stdinArgs {
			return Dangerous("xargs 从标准输入构建命令")
		}
		return Dangerous(name + " 缺少要执行的命令")
	}
	nested := strings.Join(args[index:], " ")
	if wrapper.stdinArgs {
		if stdin.ok {
			nested += " " + stdin.text
		} else if recursiveDeleteCommand(args[index:]) {
			return Dangerous("xargs 以未受控输入调用递归删除")
		}
	}
	return classifyCommandDepth(nested, rules, depth+1)
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
