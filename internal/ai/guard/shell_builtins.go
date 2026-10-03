package guard

import "strings"

// gnuLongOption describes one documented long option of a GNU-style command.
type gnuLongOption struct {
	name          string // option name without the leading "--"
	takesValue    bool   // value may be attached (=value) or given as the next argument
	optionalValue bool   // value only accepted attached (e.g. --check)
}

// resolveGNULongOption resolves a "--name[=value]" argument against the known
// option table using GNU unique-abbreviation semantics: an exact match wins,
// otherwise a prefix is accepted only when exactly one known option starts
// with it. Unknown or ambiguous prefixes fail closed (ok == false) so callers
// never silently skip an option they do not understand.
func resolveGNULongOption(arg string, known []gnuLongOption) (option gnuLongOption, value string, attached, ok bool) {
	if !strings.HasPrefix(arg, "--") || arg == "--" {
		return gnuLongOption{}, "", false, false
	}
	name, value, attached := strings.Cut(arg[2:], "=")
	for _, candidate := range known {
		if candidate.name == name {
			return candidate, value, attached, true
		}
	}
	var matched *gnuLongOption
	ambiguous := false
	for index := range known {
		if strings.HasPrefix(known[index].name, name) {
			if matched != nil {
				ambiguous = true
			}
			matched = &known[index]
		}
	}
	if matched == nil || ambiguous {
		return gnuLongOption{}, "", false, false
	}
	return *matched, value, attached, true
}

var envLongOptions = []gnuLongOption{
	{name: "ignore-environment"},
	{name: "null"},
	{name: "unset", takesValue: true},
	{name: "chdir", takesValue: true},
	{name: "argv0", takesValue: true},
	{name: "split-string", takesValue: true},
	{name: "block-signal", optionalValue: true},
	{name: "default-signal", optionalValue: true},
	{name: "ignore-signal", optionalValue: true},
	{name: "list-signal-handling"},
	{name: "debug"},
	{name: "help"},
	{name: "version"},
}

var sortLongOptions = []gnuLongOption{
	{name: "ignore-leading-blanks"},
	{name: "dictionary-order"},
	{name: "ignore-case"},
	{name: "ignore-nonprinting"},
	{name: "merge"},
	{name: "month-sort"},
	{name: "numeric-sort"},
	{name: "general-numeric-sort"},
	{name: "human-numeric-sort"},
	{name: "version-sort"},
	{name: "natural-sort"},
	{name: "reverse"},
	{name: "sort", takesValue: true},
	{name: "random-sort"},
	{name: "random-source", takesValue: true},
	{name: "key", takesValue: true},
	{name: "field-separator", takesValue: true},
	{name: "temporary-directory", takesValue: true},
	{name: "buffer-size", takesValue: true},
	{name: "batch-size", takesValue: true},
	{name: "compress-program", takesValue: true},
	{name: "parallel", takesValue: true},
	{name: "files0-from", takesValue: true},
	{name: "output", takesValue: true},
	{name: "check", optionalValue: true},
	{name: "quiet"},
	{name: "silent"},
	{name: "stable"},
	{name: "unique"},
	{name: "zero-terminated"},
	{name: "debug"},
	{name: "help"},
	{name: "version"},
}

var suLongOptions = []gnuLongOption{
	{name: "command", takesValue: true},
	{name: "session-command", takesValue: true},
	{name: "login"},
	{name: "preserve-environment"},
	{name: "shell", takesValue: true},
	{name: "group", takesValue: true},
	{name: "supp-group", takesValue: true},
	{name: "whitelist-environment", takesValue: true},
	{name: "help"},
	{name: "version"},
}

var sudoLongOptions = []gnuLongOption{
	{name: "edit"},
	{name: "login"},
	{name: "non-interactive"},
	{name: "set-home"},
	{name: "preserve-env", optionalValue: true},
	{name: "shell"},
	{name: "background"},
	{name: "askpass"},
	{name: "stdin"},
	{name: "validate"},
	{name: "kill"},
	{name: "list"},
	{name: "reset-timestamp"},
	{name: "preserve-group-vector"},
	{name: "help"},
	{name: "version"},
	{name: "user", takesValue: true},
	{name: "group", takesValue: true},
	{name: "host", takesValue: true},
	{name: "prompt", takesValue: true},
	{name: "chdir", takesValue: true},
	{name: "command-timeout", takesValue: true},
	{name: "type", takesValue: true},
	{name: "role", takesValue: true},
	{name: "close-from", takesValue: true},
}

var systemctlLongOptions = []gnuLongOption{
	{name: "host", takesValue: true},
	{name: "machine", takesValue: true},
	{name: "type", takesValue: true},
	{name: "output", takesValue: true},
	{name: "job-mode", takesValue: true},
	{name: "root", takesValue: true},
	{name: "image", takesValue: true},
	{name: "image-policy", takesValue: true},
	{name: "state", takesValue: true},
	{name: "preset-mode", takesValue: true},
	{name: "kill-who", takesValue: true},
	{name: "signal", takesValue: true},
	{name: "lines", takesValue: true},
	{name: "user"},
	{name: "system"},
	{name: "global"},
	{name: "no-block"},
	{name: "wait"},
	{name: "quiet"},
	{name: "failed"},
	{name: "plain"},
	{name: "all"},
	{name: "full"},
	{name: "recursive"},
	{name: "no-legend"},
	{name: "no-pager"},
	{name: "show-transaction"},
	{name: "dry-run"},
	{name: "help"},
	{name: "version"},
}

var kubectlExecLongOptions = []gnuLongOption{
	{name: "container", takesValue: true},
	{name: "namespace", takesValue: true},
	{name: "context", takesValue: true},
	{name: "kubeconfig", takesValue: true},
	{name: "pod-running-timeout", takesValue: true},
	{name: "stdin"},
	{name: "tty"},
	{name: "quiet"},
}

// sudoValueShorts lists the sudo/doas short options that consume a value,
// matching privilegeOptionValue and stripCommandFlags.
const sudoValueShorts = "ughpaCDTtUGR"

func quoteShellToken(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shortOptionValue(arg string, option byte) (string, bool) {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return "", false
	}
	for index := 1; index < len(arg); index++ {
		if arg[index] == option {
			return arg[index+1:], true
		}
	}
	return "", false
}

func classifyStateChangingBuiltins(name string, args []string) (Ruling, bool) {
	switch name {
	case "sort":
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				break
			}
			if strings.HasPrefix(arg, "--") {
				option, _, attached, ok := resolveGNULongOption(arg, sortLongOptions)
				if !ok {
					return Indeterminate("sort 包含无法识别的长选项"), true
				}
				switch option.name {
				case "output":
					return Confirm(KindWriteFS, "sort 输出到文件"), true
				case "compress-program":
					return Dangerous("sort 调用外部程序，内容无法验证"), true
				}
				if option.takesValue && !attached && !option.optionalValue {
					i++
				}
				continue
			}
			if value, ok := shortOptionValue(arg, 'o'); ok && value != "-" {
				return Confirm(KindWriteFS, "sort 输出到文件"), true
			}
		}
		return Allow(), true
	case "xxd":
		positional := 0
		skipValue := false
		for _, arg := range args {
			if skipValue {
				skipValue = false
				continue
			}
			if arg == "-r" || arg == "-revert" || arg == "--revert" {
				return Confirm(KindWriteFS, "xxd 反向转换会写入输出文件"), true
			}
			switch arg {
			case "-c", "-g", "-l", "-s":
				skipValue = true
				continue
			}
			if !strings.HasPrefix(arg, "-") {
				positional++
			}
		}
		if positional > 1 {
			return Confirm(KindWriteFS, "xxd 指定了输出文件"), true
		}
		return Allow(), true
	case "hostname":
		if len(args) == 0 {
			return Allow(), true
		}
		return Confirm(KindService, "主机名变更"), true
	case "hostnamectl":
		if len(args) == 0 || len(args) == 1 && args[0] == "status" {
			return Allow(), true
		}
		return Confirm(KindService, "主机名或系统信息变更"), true
	case "date":
		skipNext := false
		for i, arg := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if arg == "-d" || arg == "--date" || arg == "-r" || arg == "--reference" {
				skipNext = true
				continue
			}
			if arg == "-s" || strings.HasPrefix(arg, "--set") {
				return Confirm(KindService, "系统时间变更"), true
			}
			if !strings.HasPrefix(arg, "-") || i > 0 && (args[i-1] == "-d" || args[i-1] == "--date") {
				return Confirm(KindService, "系统时间变更"), true
			}
		}
		return Allow(), true
	case "journalctl":
		for _, arg := range args {
			if arg == "--rotate" || strings.HasPrefix(arg, "--vacuum-") {
				return Confirm(KindService, "日志清理或轮转"), true
			}
		}
		return Allow(), true
	case "dmesg":
		if containsAnyFold(args, "-c", "-C", "--clear") {
			return Confirm(KindService, "内核日志清理"), true
		}
		return Allow(), true
	case "ip":
		for _, arg := range args {
			switch strings.ToLower(arg) {
			case "add", "del", "delete", "set", "change", "replace", "save", "restore", "exec":
				return Confirm(KindService, "网络配置或网络命名空间执行"), true
			}
		}
		return Allow(), true
	case "ifconfig":
		if len(args) > 1 {
			return Confirm(KindService, "网络接口配置变更"), true
		}
		return Allow(), true
	case "route":
		if containsAnyFold(args, "add", "del", "delete", "change") {
			return Confirm(KindService, "路由配置变更"), true
		}
		return Allow(), true
	case "arp":
		if containsAnyFold(args, "-d", "-s", "-f", "--delete", "--set") {
			return Confirm(KindService, "ARP 配置变更"), true
		}
		return Allow(), true
	default:
		return Ruling{}, false
	}
}
