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
	{name: "split-string", takesValue: true},
	{name: "debug"},
	{name: "help"},
	{name: "version"},
}

var sortLongOptions = []gnuLongOption{
	{name: "ignore-leading-blanks"},
	{name: "dictionary-order"},
	{name: "ignore-case"},
	{name: "ignore-nonprinting"},
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

func envSplitString(arg string) (string, bool, bool) {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-" {
		return "", false, false
	}
	for index := 1; index < len(arg); index++ {
		switch arg[index] {
		case 'S':
			if index+1 == len(arg) {
				return "", false, true
			}
			return arg[index+1:], true, true
		case 'u', 'C':
			return "", false, false
		}
	}
	return "", false, false
}

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

func classifyStateChangingBuiltins(name string, args, rules []string, depth int) (Ruling, bool) {
	switch name {
	case "env":
		splitString := func(payload string, rest []string) (Ruling, bool) {
			payload = strings.ReplaceAll(payload, "\\_", " ")
			for _, extra := range rest {
				payload += " " + quoteShellToken(extra)
			}
			return Worst(Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), classifyCommandDepth(payload, rules, depth+1)), true
		}
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				if i+1 < len(args) {
					return classifySimple(args[i+1:], nil, rules, depth), true
				}
				return Allow(), true
			}
			if strings.HasPrefix(arg, "--") {
				option, value, attached, ok := resolveGNULongOption(arg, envLongOptions)
				if !ok {
					return Confirm(KindUnknown, "env 包含无法识别的长选项"), true
				}
				switch option.name {
				case "split-string":
					if !attached {
						if i+1 >= len(args) {
							return Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), true
						}
						i++
						value = args[i]
					}
					return splitString(value, args[i+1:])
				case "unset", "chdir":
					if !attached {
						i++
					}
				}
				continue
			}
			if payload, attached, ok := envSplitString(arg); ok {
				if !attached {
					if i+1 >= len(args) {
						return Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), true
					}
					i++
					payload = args[i]
				}
				return splitString(payload, args[i+1:])
			}
			if isAssignment(arg) {
				continue
			}
			if arg == "-u" || arg == "-C" {
				i++
				continue
			}
			if arg == "-i" || arg == "-0" {
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return Confirm(KindUnknown, "env 包含无法识别的选项"), true
			}
			return classifySimple(args[i:], nil, rules, depth), true
		}
		return Allow(), true
	case "sort":
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				break
			}
			if strings.HasPrefix(arg, "--") {
				option, _, attached, ok := resolveGNULongOption(arg, sortLongOptions)
				if !ok {
					return Confirm(KindUnknown, "sort 包含无法识别的长选项"), true
				}
				switch option.name {
				case "output":
					return Confirm(KindWriteFS, "sort 输出到文件"), true
				case "compress-program":
					return Confirm(KindUnknown, "sort 调用外部压缩程序"), true
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
