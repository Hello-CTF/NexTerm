package guard

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type shellToken struct {
	text string
	op   bool
}

type shellSegment struct {
	tokens []string
	ops    []string
	raw    string
}

func ClassifyCommand(command string, rules []string) Ruling {
	return classifyCommandDepth(command, rules, 0)
}

func classifyCommandDepth(command string, rules []string, depth int) Ruling {
	if depth > 8 {
		return Dangerous("命令嵌套过深，无法安全分析")
	}
	result := Allow()
	if matchDangerRule(command, rules) {
		result = Dangerous("命中自定义危险规则")
	}
	segments, substitutions, err := parseShell(command)
	if err != nil {
		result = Worst(result, Confirm(KindUnknown, "命令无法可靠解析: "+err.Error()))
	}
	for _, substitution := range substitutions {
		result = Worst(result, Confirm(KindUnknown, "包含命令替换"), classifyCommandDepth(substitution, rules, depth+1))
	}
	if len(segments) == 0 {
		return result
	}
	for _, segment := range segments {
		if len(segment.tokens) == 0 {
			if segment.hasWriteRedirection() {
				result = Worst(result, classifyRedirection(segment.ops))
			}
			continue
		}
		result = Worst(result, classifySegment(segment, rules, depth))
		if result.Risk == Forbidden {
			return result
		}
	}
	return result
}

func parseShell(input string) ([]shellSegment, []string, error) {
	var segments []shellSegment
	var current shellSegment
	var word strings.Builder
	var substitutions []string
	var quote rune
	escaped := false
	wordSeen := false
	flushWord := func() {
		if wordSeen || word.Len() != 0 {
			current.tokens = append(current.tokens, word.String())
			word.Reset()
			wordSeen = false
		}
	}
	flushSegment := func() {
		flushWord()
		if len(current.tokens) != 0 || len(current.ops) != 0 {
			current.raw = strings.TrimSpace(current.raw)
			segments = append(segments, current)
		}
		current = shellSegment{}
	}
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		current.raw += string(r)
		if escaped {
			word.WriteRune(r)
			wordSeen = true
			escaped = false
			continue
		}
		if quote != 0 {
			if r == '\\' && quote == '"' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
				wordSeen = true
				continue
			}
			if quote == '"' && r == '`' {
				value, next, ok := readBacktick(runes, i+1)
				if !ok {
					return segments, substitutions, strconv.ErrSyntax
				}
				substitutions = append(substitutions, value)
				word.WriteString("$(" + value + ")")
				i = next
				continue
			}
			word.WriteRune(r)
			wordSeen = true
			continue
		}
		switch r {
		case '\\':
			escaped = true
			wordSeen = true
		case '\'', '"':
			quote = r
			wordSeen = true
		case '`':
			value, next, ok := readBacktick(runes, i+1)
			if !ok {
				return segments, substitutions, strconv.ErrSyntax
			}
			substitutions = append(substitutions, value)
			word.WriteString("$(" + value + ")")
			wordSeen = true
			i = next
		case '$':
			if i+1 < len(runes) && runes[i+1] == '(' {
				value, next, ok := readBalanced(runes, i+2)
				if !ok {
					return segments, substitutions, strconv.ErrSyntax
				}
				substitutions = append(substitutions, value)
				word.WriteString("$(" + value + ")")
				wordSeen = true
				i = next
			} else {
				word.WriteRune(r)
				wordSeen = true
			}
		case '#':
			if !wordSeen && word.Len() == 0 {
				for i+1 < len(runes) && runes[i+1] != '\n' {
					i++
				}
			} else {
				word.WriteRune(r)
			}
		case ' ', '\t', '\r':
			flushWord()
		case '\n', ';', '|', '&':
			flushWord()
			op := string(r)
			if i+1 < len(runes) && ((r == '|' && runes[i+1] == '|') || (r == '&' && runes[i+1] == '&')) {
				i++
				current.raw += string(runes[i])
				op += string(r)
			}
			current.ops = append(current.ops, op)
			segments = append(segments, current)
			current = shellSegment{}
		case '>', '<':
			flushWord()
			op := string(r)
			if i+1 < len(runes) && (runes[i+1] == r || r == '>' && runes[i+1] == '&') {
				i++
				current.raw += string(runes[i])
				op += string(runes[i])
			}
			current.ops = append(current.ops, op)
		default:
			word.WriteRune(r)
			wordSeen = true
		}
	}
	if escaped || quote != 0 {
		return segments, substitutions, strconv.ErrSyntax
	}
	flushSegment()
	return segments, substitutions, nil
}

func readBacktick(runes []rune, start int) (string, int, bool) {
	var value strings.Builder
	escaped := false
	for i := start; i < len(runes); i++ {
		if escaped {
			value.WriteRune(runes[i])
			escaped = false
			continue
		}
		if runes[i] == '\\' {
			escaped = true
			continue
		}
		if runes[i] == '`' {
			return value.String(), i, true
		}
		value.WriteRune(runes[i])
	}
	return "", start, false
}

func readBalanced(runes []rune, start int) (string, int, bool) {
	depth := 1
	var quote rune
	escaped := false
	var value strings.Builder
	for i := start; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			value.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			value.WriteRune(r)
			escaped = true
			continue
		}
		if quote != 0 {
			value.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			value.WriteRune(r)
			continue
		}
		if r == '(' {
			depth++
		}
		if r == ')' {
			depth--
			if depth == 0 {
				return value.String(), i, true
			}
		}
		value.WriteRune(r)
	}
	return "", start, false
}

func (s shellSegment) hasWriteRedirection() bool {
	writes := 0
	for _, op := range s.ops {
		if strings.HasPrefix(op, ">") && op != ">&" {
			writes++
		}
	}
	if writes == 1 && containsAny(s.tokens, "/dev/null") {
		return false
	}
	return writes != 0
}

func classifyRedirection(ops []string) Ruling {
	for _, op := range ops {
		if strings.HasPrefix(op, ">") {
			return Confirm(KindWriteFS, "包含文件重定向")
		}
	}
	return Confirm(KindUnknown, "包含无法确认安全性的 shell 操作")
}

func classifySegment(segment shellSegment, rules []string, depth int) Ruling {
	tokens := stripDescriptors(segment.tokens)
	for len(tokens) > 0 && isAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return Allow()
	}
	result := Allow()
	if segment.hasWriteRedirection() {
		result = Confirm(KindWriteFS, "包含文件重定向")
		for i, token := range tokens {
			if isBlockDevice(token) && i > 0 {
				return Deny("禁止直接写入块设备")
			}
		}
	}
	for len(tokens) > 0 {
		name := commandName(tokens[0])
		if name == "sudo" || name == "doas" {
			result = Worst(result, Confirm(KindSudo, "包含权限提升"))
			tokens = stripCommandFlags(tokens[1:])
			continue
		}
		if name == "su" {
			return Worst(result, Confirm(KindSudo, "包含身份切换"))
		}
		break
	}
	if len(tokens) == 0 {
		return result
	}
	for len(tokens) > 0 && isAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return result
	}
	return Worst(result, classifySimple(tokens, segment.ops, rules, depth))
}

func stripDescriptors(tokens []string) []string {
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) > 2 && (strings.HasSuffix(token, ">") || strings.HasSuffix(token, "<")) {
			if _, err := strconv.Atoi(token[:len(token)-1]); err == nil {
				continue
			}
		}
		result = append(result, token)
	}
	return result
}

func stripCommandFlags(tokens []string) []string {
	for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
		tokens = tokens[1:]
	}
	return tokens
}

func isAssignment(value string) bool {
	index := strings.IndexByte(value, '=')
	if index <= 0 {
		return false
	}
	for i, r := range value[:index] {
		if !(r == '_' || unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func commandName(value string) string {
	return strings.ToLower(filepath.Base(strings.ReplaceAll(value, "\\", "/")))
}

func classifySimple(tokens []string, ops []string, rules []string, depth int) Ruling {
	name := commandName(tokens[0])
	args := tokens[1:]
	if isInterpreter(name) {
		return Worst(Confirm(KindUnknown, "解释器或脚本执行需要确认"), classifyInterpreter(name, args, rules, depth))
	}
	if ruling, ok := forbiddenCommand(name, args); ok {
		return ruling
	}
	if ruling, ok := dangerousCommand(name, args); ok {
		return ruling
	}
	if matchDangerRule(strings.Join(tokens, " "), rules) {
		return Dangerous("命中自定义危险规则")
	}
	if ruling, ok := classifyStateChangingBuiltins(name, args); ok {
		return ruling
	}
	switch name {
	case "rm", "rmdir", "mv", "cp", "dd", "mkfs", "mkfs.ext4", "mkfs.xfs", "shred", "truncate":
		return Confirm(KindWriteFS, "文件系统写操作")
	case "touch", "mkdir", "install", "ln", "unlink":
		return Confirm(KindWriteFS, "文件系统写操作")
	case "reboot", "shutdown", "poweroff", "halt":
		return Dangerous("系统关机或重启")
	case "init", "telinit":
		if containsAny(args, "0", "6") {
			return Dangerous("系统关机或重启")
		}
		return Confirm(KindService, "系统运行级别变更")
	case "systemctl", "service", "rc-service", "launchctl", "sc", "net", "ufw", "firewall-cmd", "iptables", "nft":
		return classifyService(name, args)
	case "apt", "apt-get", "yum", "dnf", "zypper", "pacman", "dpkg", "rpm", "apk", "brew", "choco", "winget":
		return classifyPackage(args)
	case "kill", "pkill", "killall":
		return Confirm(KindProcess, "进程控制")
	case "chmod", "chown", "chgrp", "chattr", "setfacl", "useradd", "userdel", "usermod", "passwd", "crontab":
		return Confirm(KindPermission, "权限或账户变更")
	case "docker", "podman":
		return classifyDocker(args, rules, depth)
	case "kubectl":
		return classifyKubectl(args)
	case "git":
		return classifyGit(args)
	case "redis-cli", "valkey-cli":
		return classifyRedis(args)
	case "mysql", "mariadb", "psql":
		return Confirm(KindDBWrite, "交互式数据库客户端需要确认")
	case "sed", "awk", "gawk", "ed", "vim", "vi", "nano", "emacs":
		return Confirm(KindUnknown, "命令具有编辑或执行子命令能力")
	case "find":
		if containsAnyFold(args, "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls") {
			return Confirm(KindWriteFS, "find 包含写入或执行动作")
		}
		return Allow()
	case "tar":
		for _, arg := range args {
			lower := strings.ToLower(arg)
			if lower == "--to-command" || lower == "--use-compress-program" || lower == "--checkpoint-action" || strings.HasPrefix(lower, "--checkpoint-action=") || strings.HasPrefix(lower, "--use-compress-program=") {
				return Confirm(KindUnknown, "tar 包含外部命令执行")
			}
		}
		if !containsAny(args, "-t", "-tf", "--list") && !hasShortFlag(args, 't') {
			return Confirm(KindWriteFS, "压缩包解包或创建需要确认")
		}
		return Allow()
	case "gzip", "gunzip", "bzip2", "xz", "zip":
		return Confirm(KindWriteFS, "压缩工具可能修改文件")
	case "unzip":
		if containsAny(args, "-l", "-Z", "--list") {
			return Allow()
		}
		return Confirm(KindWriteFS, "解压会写入文件")
	case "curl", "wget":
		return classifyDownload(name, args, ops)
	case "ssh", "scp", "sftp", "rsync":
		return Confirm(KindUnknown, "远程传输或执行需要确认")
	}
	if safeFirstToken[name] {
		return Allow()
	}
	return Confirm(KindUnknown, "未列入只读白名单的命令")
}

var safeFirstToken = func() map[string]bool {
	values := strings.Fields(`ls ll pwd cat head tail tac nl grep egrep fgrep rg cut sort uniq wc stat file tree diff comm jq xxd hexdump od strings ps pidof df du free vmstat iostat lscpu lsblk uname whoami id hostname hostnamectl uptime date cal env printenv which whereis type echo printf dmesg journalctl ss netstat ip ifconfig arp route ping traceroute dig nslookup host lsof top htop atop sar w who last history md5sum sha1sum sha256sum zcat zipinfo basename dirname readlink realpath getent ulimit tput resize lsb_release arch nproc true false sleep clear select show describe desc explain cd pushd popd`)
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}()

func isInterpreter(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "ksh", "dash", "fish", "python", "python2", "python3", "node", "nodejs", "deno", "bun", "perl", "ruby", "php", "lua", "osascript", "powershell", "pwsh", "cmd", "cmd.exe":
		return true
	default:
		return false
	}
}

func classifyInterpreter(name string, args []string, rules []string, depth int) Ruling {
	result := Allow()
	for i, arg := range args {
		if (arg == "-c" || arg == "-Command" || arg == "/c") && i+1 < len(args) {
			if name == "sh" || name == "bash" || name == "zsh" || name == "ksh" || name == "dash" || name == "fish" || name == "powershell" || name == "pwsh" || name == "cmd" || name == "cmd.exe" {
				result = Worst(result, classifyCommandDepth(strings.Join(args[i+1:i+2], " "), rules, depth+1))
			} else if matchDangerRule(args[i+1], rules) || containsDestructiveText(args[i+1]) {
				result = Worst(result, Dangerous("解释器代码包含危险操作"))
			}
		}
	}
	return result
}

func containsDestructiveText(value string) bool {
	lower := strings.ToLower(strings.ReplaceAll(value, " ", ""))
	for _, text := range []string{"rm-rf/", "remove-item/-force", "rmtree(\"/\")", "dropdatabase", "flushall"} {
		if strings.Contains(lower, text) {
			return true
		}
	}
	return false
}

func forbiddenCommand(name string, args []string) (Ruling, bool) {
	if strings.HasPrefix(name, "mkfs") {
		return Deny("禁止格式化文件系统"), true
	}
	if name == "rm" {
		recursive := false
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") && (strings.Contains(arg, "r") || strings.Contains(arg, "R")) {
				recursive = true
			}
		}
		for _, arg := range args {
			if recursive && isCriticalRoot(arg) {
				return Deny("禁止递归删除系统根目录或关键目录"), true
			}
		}
	}
	if name == "dd" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "of=") && isBlockDevice(strings.TrimPrefix(arg, "of=")) {
				return Deny("禁止直接覆写块设备"), true
			}
		}
	}
	return Ruling{}, false
}

func dangerousCommand(name string, args []string) (Ruling, bool) {
	if name == "chmod" && containsAny(args, "-R", "--recursive") && containsAny(args, "777", "0777", "a+rwx") {
		for _, arg := range args {
			if isCriticalRoot(arg) {
				return Dangerous("递归放开关键目录权限"), true
			}
		}
	}
	return Ruling{}, false
}

func isCriticalRoot(value string) bool {
	value = strings.TrimSpace(strings.Trim(value, "'\""))
	for strings.HasPrefix(value, "-") {
		return false
	}
	value = strings.TrimRight(value, "/")
	if value == "" || value == "~" || value == "$HOME" || value == "${HOME}" {
		return true
	}
	switch value {
	case "/", "/*", "/~", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/home", "/sbin", "/sys", "/usr", "/var", "C:", "c:":
		return true
	default:
		return strings.HasPrefix(value, "/home/") || strings.HasPrefix(value, "/root/")
	}
}

func isBlockDevice(value string) bool {
	value = strings.Trim(value, "'\"")
	for _, prefix := range []string{"/dev/sd", "/dev/hd", "/dev/nvme", "/dev/mapper/", "/dev/vd", "/dev/xvd"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func classifyService(name string, args []string) Ruling {
	if name == "systemctl" && len(args) > 0 {
		switch args[0] {
		case "status", "show", "list-units", "list-unit-files", "is-active", "is-enabled", "--failed", "list-dependencies", "cat":
			return Allow()
		}
	}
	if name == "service" && len(args) > 1 && args[1] == "status" {
		return Allow()
	}
	return Confirm(KindService, "服务或防火墙配置变更")
}

func classifyPackage(args []string) Ruling {
	if len(args) > 0 {
		switch args[0] {
		case "list", "search", "info", "show", "--version", "version":
			return Allow()
		}
	}
	return Confirm(KindPackage, "软件包管理操作")
}

func classifyDocker(args []string, rules []string, depth int) Ruling {
	if len(args) == 0 {
		return Confirm(KindUnknown, "docker 子命令不明确")
	}
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		index++
	}
	if index >= len(args) {
		return Allow("docker 全局信息查询")
	}
	sub := strings.ToLower(args[index])
	rest := args[index+1:]
	switch sub {
	case "ps", "images", "logs", "inspect", "version", "info", "port", "top", "diff":
		return Allow()
	case "stats":
		if containsAny(rest, "--no-stream") {
			return Allow()
		}
		return Confirm(KindUnknown, "持续 Docker 统计需要确认")
	case "exec":
		result := Confirm(KindDockerMutate, "容器内执行命令")
		if shellIndex := indexOfAny(rest, "sh", "bash", "zsh", "ash"); shellIndex >= 0 && shellIndex+2 < len(rest) && (rest[shellIndex+1] == "-c" || rest[shellIndex+1] == "-lc") {
			result = Worst(result, classifyCommandDepth(rest[shellIndex+2], rules, depth+1))
		} else if len(rest) > 1 {
			result = Worst(result, Confirm(KindDockerMutate, "容器内命令需要确认"))
		}
		return result
	case "run", "create", "start", "stop", "restart", "rm", "kill", "pause", "unpause", "rename", "update", "cp", "commit", "import", "load", "pull", "push", "build", "tag", "rmi", "save", "attach":
		if (sub == "rm" || sub == "rmi") && containsAnyFold(rest, "-f", "--force") && containsAnyFold(rest, "$(docker", "-aq") {
			return Dangerous("强制删除全部 Docker 资源")
		}
		return Confirm(KindDockerMutate, "Docker 资源变更")
	case "prune":
		return Dangerous("Docker 清理会删除资源")
	case "system", "container", "image", "volume", "network", "compose", "builder", "buildx":
		if len(rest) == 0 {
			return Confirm(KindDockerMutate, "Docker 资源操作")
		}
		nested := strings.ToLower(rest[0])
		switch nested {
		case "ls", "inspect", "ps", "config", "logs", "version":
			return Allow()
		case "prune":
			return Dangerous("Docker 清理会删除资源")
		case "exec", "run":
			return classifyDocker(append([]string{nested}, rest[1:]...), rules, depth)
		default:
			return Confirm(KindDockerMutate, "Docker 资源变更")
		}
	default:
		return Confirm(KindUnknown, "未知 Docker 子命令")
	}
}

func classifyKubectl(args []string) Ruling {
	if len(args) > 0 {
		switch args[0] {
		case "get", "describe", "logs", "top", "version", "cluster-info", "api-resources", "api-versions":
			return Allow()
		case "config":
			if len(args) > 1 {
				switch args[1] {
				case "view", "current-context", "get-contexts", "get-clusters", "get-users":
					return Allow()
				}
			}
			return Confirm(KindService, "kubectl 配置变更")
		case "delete", "drain", "cordon", "uncordon", "apply", "create", "replace", "patch", "scale", "rollout", "exec", "port-forward", "cp", "edit", "set", "label", "annotate", "taint":
			return Confirm(KindService, "Kubernetes 资源或工作负载变更")
		}
	}
	return Confirm(KindUnknown, "未知 kubectl 子命令")
}

func classifyGit(args []string) Ruling {
	if len(args) == 0 {
		return Allow()
	}
	if containsAnyFold(args[1:], "--output", "-o", "--exec") {
		return Confirm(KindWriteFS, "Git 输出文件或扩展命令")
	}
	switch args[0] {
	case "status", "log", "diff", "show", "rev-parse", "ls-files", "blame", "shortlog", "describe":
		return Allow()
	case "branch":
		if len(args) == 1 {
			return Allow()
		}
		for _, arg := range args[1:] {
			switch arg {
			case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--list", "--show-current":
			default:
				if !strings.HasPrefix(arg, "-") || arg == "-d" || arg == "-D" || arg == "-m" || arg == "-M" || arg == "-c" || arg == "-C" {
					return Confirm(KindWriteFS, "Git 分支创建、删除或改名")
				}
			}
		}
		return Allow()
	case "remote":
		if len(args) == 1 || len(args) == 2 && (args[1] == "-v" || args[1] == "--verbose" || args[1] == "show" || args[1] == "get-url") {
			return Allow()
		}
		return Confirm(KindWriteFS, "Git 远端配置变更")
	case "push":
		if containsAnyFold(args[1:], "--force", "-f", "--force-with-lease") {
			return Confirm(KindWriteFS, "Git 强制推送")
		}
		return Confirm(KindWriteFS, "Git 推送")
	case "clean", "reset", "checkout", "switch", "restore", "rm", "mv", "commit", "merge", "rebase", "cherry-pick", "revert", "am", "apply", "config", "clone", "fetch", "pull", "submodule", "worktree", "stash", "tag", "add":
		return Confirm(KindWriteFS, "Git 仓库或远端变更")
	default:
		return Confirm(KindUnknown, "未知 Git 子命令")
	}
}

func classifyDownload(name string, args, ops []string) Ruling {
	for _, op := range ops {
		if op == "|" || op == "||" {
			return Confirm(KindShellPipe, "网络内容通过管道交给其他命令")
		}
	}
	for i, arg := range args {
		lower := strings.ToLower(arg)
		switch lower {
		case "-o", "-O", "--output", "--output-document", "-d", "--data", "--data-binary", "--data-raw", "--form", "-F", "-T", "--upload-file", "--post-data", "--post-file", "--method", "--json":
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		case "-x", "-X", "--request":
			if i+1 < len(args) && !strings.EqualFold(args[i+1], "GET") {
				return Confirm(KindWriteFS, "非 GET 网络请求")
			}
		}
		if strings.HasPrefix(lower, "--request=") && !strings.EqualFold(strings.TrimPrefix(lower, "--request="), "get") {
			return Confirm(KindWriteFS, "非 GET 网络请求")
		}
		if strings.HasPrefix(lower, "--json=") {
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		}
		if strings.HasPrefix(lower, "--data=") || strings.HasPrefix(lower, "--output=") || strings.HasPrefix(lower, "--upload-file=") || strings.HasPrefix(lower, "--output-document=") || strings.HasPrefix(lower, "--post-data=") || strings.HasPrefix(lower, "--post-file=") || strings.HasPrefix(lower, "--method=") {
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		}
	}
	if name == "wget" {
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "http") {
				continue
			}
		}
		if !containsAny(args, "-qO-", "-O-", "--spider", "--server-response", "-S") {
			return Confirm(KindWriteFS, "wget 默认会保存文件")
		}
	}
	return Allow()
}

func classifyRedis(args []string) Ruling {
	if containsAny(args, "--scan") {
		return Allow()
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "-p" || arg == "-a" || arg == "--user" || arg == "--pass" || arg == "-n" || arg == "-u" {
				i++
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	if len(filtered) == 0 {
		return Confirm(KindUnknown, "交互式 Redis 客户端需要确认")
	}
	command := strings.ToUpper(filtered[0])
	switch command {
	case "PING", "INFO", "DBSIZE", "EXISTS", "TYPE", "TTL", "PTTL", "GET", "MGET", "GETRANGE", "STRLEN", "HGET", "HGETALL", "HMGET", "HLEN", "HKEYS", "HVALS", "HEXISTS", "LRANGE", "LLEN", "LINDEX", "SMEMBERS", "SISMEMBER", "SMISMEMBER", "SCARD", "SRANDMEMBER", "ZRANGE", "ZRANK", "ZSCORE", "ZCARD", "ZCOUNT", "XRANGE", "XREVRANGE", "XLEN", "SCAN", "SSCAN", "HSCAN", "ZSCAN", "RANDOMKEY", "TIME", "OBJECT", "DUMP", "DEBUG":
		if command == "DEBUG" {
			return Confirm(KindDBWrite, "Redis DEBUG 可能改变服务状态")
		}
		return Allow()
	case "CLIENT":
		if len(filtered) > 1 {
			switch strings.ToUpper(filtered[1]) {
			case "LIST", "INFO", "GETNAME", "ID":
				return Allow()
			}
		}
		return Confirm(KindDBWrite, "Redis CLIENT 子命令需要确认")
	case "MEMORY":
		if len(filtered) > 1 && (strings.EqualFold(filtered[1], "USAGE") || strings.EqualFold(filtered[1], "STATS") || strings.EqualFold(filtered[1], "DOCTOR")) {
			return Allow()
		}
		return Confirm(KindDBWrite, "Redis MEMORY 子命令需要确认")
	case "FLUSHALL", "FLUSHDB", "SHUTDOWN":
		return Dangerous("Redis 数据或服务破坏性操作")
	default:
		return Confirm(KindDBWrite, "Redis 写入或未知命令")
	}
}

func matchDangerRule(command string, rules []string) bool {
	lower := strings.ToLower(command)
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule != "" && strings.Contains(lower, strings.ToLower(rule)) {
			return true
		}
	}
	return false
}

func containsAny(values []string, targets ...string) bool {
	for _, value := range values {
		for _, target := range targets {
			if value == target {
				return true
			}
		}
	}
	return false
}

func containsAnyFold(values []string, targets ...string) bool {
	for _, value := range values {
		for _, target := range targets {
			if strings.EqualFold(value, target) {
				return true
			}
		}
	}
	return false
}

func hasShortFlag(args []string, flag rune) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg, flag) {
			return true
		}
	}
	return false
}

func indexOfAny(values []string, targets ...string) int {
	for i, value := range values {
		for _, target := range targets {
			if commandName(value) == target {
				return i
			}
		}
	}
	return -1
}
