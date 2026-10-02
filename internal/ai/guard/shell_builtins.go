package guard

import "strings"

func classifyStateChangingBuiltins(name string, args []string) (Ruling, bool) {
	switch name {
	case "env":
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "-S" || arg == "--split-string" || arg == "-vS" {
				return Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), true
			}
			if strings.HasPrefix(arg, "-vS") && len(arg) > 3 {
				return Worst(Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), classifyCommandDepth(arg[3:], nil, 0)), true
			}
			if strings.HasPrefix(arg, "-S") && len(arg) > 2 {
				return Worst(Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), classifyCommandDepth(arg[2:], nil, 0)), true
			}
			if strings.HasPrefix(arg, "--split-string=") {
				return Worst(Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), classifyCommandDepth(strings.TrimPrefix(arg, "--split-string="), nil, 0)), true
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
			return classifySimple(args[i:], nil, nil, 0), true
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
