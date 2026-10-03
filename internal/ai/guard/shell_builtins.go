package guard

import "strings"

func envSplitString(arg string) (string, bool, bool) {
	if arg == "-S" || arg == "--split-string" {
		return "", false, true
	}
	if strings.HasPrefix(arg, "--split-string=") {
		return strings.TrimPrefix(arg, "--split-string="), true, true
	}
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
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
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if payload, attached, ok := envSplitString(arg); ok {
				if !attached {
					if i+1 >= len(args) {
						return Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), true
					}
					i++
					payload = args[i]
				}
				for _, arg := range args[i+1:] {
					payload += " " + quoteShellToken(arg)
				}
				return Worst(Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), classifyCommandDepth(payload, rules, depth+1)), true
			}
			if isAssignment(arg) {
				continue
			}
			if arg == "-u" || arg == "--unset" || arg == "-C" || arg == "--chdir" {
				i++
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			return classifySimple(args[i:], nil, rules, depth), true
		}
		return Allow(), true
	case "sort":
		for _, arg := range args {
			if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=") {
				return Confirm(KindWriteFS, "sort 输出到文件"), true
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
