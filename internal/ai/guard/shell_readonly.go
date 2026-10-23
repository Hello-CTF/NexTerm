package guard

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// versionQuery reports a lone version argument to a program NexTerm has no
// rule for; such a call prints a version and exits.
func versionQuery(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--version", "-version", "-V", "version":
		return true
	}
	return false
}

// interpreterVersionFlags lists, per interpreter, the lone flags that print the
// version and exit. Other flags (python -v, ruby -v) start verbose sessions.
var interpreterVersionFlags = map[string][]string{
	"python": {"--version", "-V"}, "python2": {"--version", "-V"}, "python3": {"--version", "-V"},
	"node": {"--version", "-v"}, "nodejs": {"--version", "-v"}, "deno": {"--version", "-V"}, "bun": {"--version", "-v"},
	"php": {"--version", "-v"}, "perl": {"--version", "-v"}, "ruby": {"--version"},
	"bash": {"--version"}, "zsh": {"--version"}, "fish": {"--version"},
}

func interpreterVersionQuery(name string, args []string) bool {
	if len(args) != 1 {
		return false
	}
	for _, flag := range interpreterVersionFlags[name] {
		if args[0] == flag {
			return true
		}
	}
	return false
}

func firstOperand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// classifySystemSetting covers hostnamectl, timedatectl and localectl: their
// status and list verbs only read, every other verb changes the system.
func classifySystemSetting(args []string) Ruling {
	verb := firstOperand(args)
	if verb == "" || verb == "status" || verb == "show" || verb == "timesync-status" || verb == "show-timesync" || strings.HasPrefix(verb, "list-") {
		return Allow()
	}
	return Confirm(KindService, "系统设置变更")
}

func classifyMount(args []string) Ruling {
	for _, arg := range args {
		if arg != "-l" {
			return Confirm(KindService, "挂载或卸载文件系统")
		}
	}
	return Allow()
}

func classifyNginx(argv0 string, args []string) Ruling {
	for _, arg := range args {
		switch arg {
		case "-t", "-T", "-v", "-V", "-q", "-h", "-?":
		case "-s":
			return Confirm(KindService, "向 nginx 发送控制信号")
		default:
			return ConfirmProgram(programKey(argv0), "nginx 的这个参数没有对应规则")
		}
	}
	return Allow()
}

// classifyLanguagePackage covers pip and the Node package managers.
func classifyLanguagePackage(argv0, name string, args []string) Ruling {
	verb := firstOperand(args)
	if verb == "" && len(args) == 1 && (args[0] == "--version" || args[0] == "-V" || args[0] == "-v") {
		return Allow()
	}
	readOnly := map[string]bool{"list": true, "ls": true, "show": true, "freeze": true, "check": true, "view": true, "info": true, "outdated": true, "why": true, "root": true}
	if name == "yarn" && verb == "" {
		return Confirm(KindPackage, "软件包管理操作")
	}
	switch {
	case readOnly[verb]:
		return Allow()
	case verb == "install" || verb == "i" || verb == "add" || verb == "uninstall" || verb == "remove" || verb == "rm" || verb == "update" || verb == "upgrade" || verb == "ci" || verb == "download":
		return Confirm(KindPackage, "软件包管理操作")
	}
	return ConfirmProgram(programKey(argv0), name+" 的这个子命令没有对应规则")
}

func classifyGoTool(argv0 string, args []string) Ruling {
	switch firstOperand(args) {
	case "version":
		return Allow()
	case "env":
		if !containsAny(args, "-w", "-u") {
			return Allow()
		}
	}
	return ConfirmProgram(programKey(argv0), "go 的这个子命令会编译或运行代码")
}

// programKey names a program for session approvals: a bare name covers that
// command from PATH, while an explicit path stays bound to that exact path.
func programKey(argv0 string) string {
	if strings.ContainsAny(argv0, `/\`) {
		return argv0
	}
	return commandName(argv0)
}

var sedSafeOptions = map[string]bool{
	"-n": true, "--quiet": true, "--silent": true, "-E": true, "-r": true, "--regexp-extended": true,
	"-s": true, "--separate": true, "-u": true, "--unbuffered": true, "-z": true, "--null-data": true,
	"--posix": true, "--debug": true, "--sandbox": true,
}

// classifySed allows sed when it only prints: no in-place editing, no script
// files, and every script uses only commands that cannot write or execute.
func classifySed(argv0 string, args []string) Ruling {
	scripts := []string{}
	explicit := false
	operands := 0
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-e" || arg == "--expression":
			if index+1 >= len(args) {
				return ConfirmProgram(programKey(argv0), "sed 参数不完整")
			}
			scripts = append(scripts, args[index+1])
			explicit = true
			index++
		case strings.HasPrefix(arg, "--expression="):
			scripts = append(scripts, strings.TrimPrefix(arg, "--expression="))
			explicit = true
		case arg == "--":
			if !explicit && index+1 < len(args) {
				scripts = append(scripts, args[index+1])
			}
			index = len(args)
		case strings.HasPrefix(arg, "-") && arg != "-":
			if !sedSafeOptions[arg] && !sedSafeShortCluster(arg) {
				return ConfirmProgram(programKey(argv0), "sed 会就地修改文件或读取脚本文件")
			}
		default:
			if !explicit && operands == 0 {
				scripts = append(scripts, arg)
			}
			operands++
		}
	}
	if len(scripts) == 0 {
		return ConfirmProgram(programKey(argv0), "sed 缺少脚本")
	}
	for _, script := range scripts {
		if !sedScriptReadOnly(script) {
			return ConfirmProgram(programKey(argv0), "sed 脚本会写文件或执行命令")
		}
	}
	return Allow()
}

func sedSafeShortCluster(arg string) bool {
	if strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return false
	}
	for _, letter := range arg[1:] {
		if !strings.ContainsRune("nErsuz", letter) {
			return false
		}
	}
	return true
}

// sedScriptReadOnly accepts the printing subset of sed: addresses, p d q =
// n N g G h H x l z D P y and s/// without the e or w flags. Anything else,
// including w, r, e, labels and branches, is rejected.
func sedScriptReadOnly(script string) bool {
	i := 0
	n := len(script)
	skipSpace := func() {
		for i < n && (script[i] == ' ' || script[i] == '\t' || script[i] == '\n' || script[i] == ';') {
			i++
		}
	}
	readDelimited := func(delim byte) bool {
		for i < n {
			switch script[i] {
			case '\\':
				i += 2
				continue
			case delim:
				i++
				return true
			case '\n':
				return false
			}
			i++
		}
		return false
	}
	readAddress := func() bool {
		switch {
		case i < n && script[i] == '$':
			i++
		case i < n && script[i] >= '0' && script[i] <= '9':
			for i < n && (script[i] >= '0' && script[i] <= '9' || script[i] == '~') {
				i++
			}
		case i < n && script[i] == '/':
			i++
			if !readDelimited('/') {
				return false
			}
			for i < n && (script[i] == 'I' || script[i] == 'M') {
				i++
			}
		}
		return true
	}
	for {
		skipSpace()
		if i >= n {
			return true
		}
		if !readAddress() {
			return false
		}
		if i < n && script[i] == ',' {
			i++
			if i < n && (script[i] == '+' || script[i] == '~') {
				i++
			}
			if !readAddress() {
				return false
			}
		}
		for i < n && (script[i] == ' ' || script[i] == '!') {
			i++
		}
		if i >= n {
			return false
		}
		command := script[i]
		i++
		switch command {
		case 'p', 'd', 'D', 'P', '=', 'n', 'N', 'g', 'G', 'h', 'H', 'x', 'l', 'z', '{', '}':
		case 'q', 'Q':
			for i < n && script[i] >= '0' && script[i] <= '9' {
				i++
			}
		case 's':
			if i >= n || script[i] == '\\' || script[i] == '\n' {
				return false
			}
			delim := script[i]
			i++
			if !readDelimited(delim) || !readDelimited(delim) {
				return false
			}
			for i < n && strings.IndexByte("gpiImM0123456789", script[i]) >= 0 {
				i++
			}
		case 'y':
			if i >= n || script[i] == '\\' || script[i] == '\n' {
				return false
			}
			delim := script[i]
			i++
			if !readDelimited(delim) || !readDelimited(delim) {
				return false
			}
		default:
			return false
		}
		if i < n && script[i] != ';' && script[i] != '\n' && script[i] != ' ' && script[i] != '}' && script[i] != '\t' {
			return false
		}
	}
}

// pureReaders only print or compare their arguments: whatever a plain variable
// expands to, they cannot write files, execute commands or change state.
var pureReaders = map[string]bool{
	"echo": true, "printf": true, "ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"stat": true, "du": true, "df": true, "basename": true, "dirname": true,
	"realpath": true, "readlink": true, "test": true, "[": true, "grep": true, "egrep": true,
	"fgrep": true, "id": true, "getent": true, "which": true, "type": true, "md5sum": true,
	"sha1sum": true, "sha256sum": true, "true": true, "false": true,
}

// plainExpansionWord reports whether a word holds only literals and plain
// parameter expansions such as $HOME or "${name}". Command, process and
// arithmetic substitutions, and expansions that assign or transform, do not count.
func plainExpansionWord(word *syntax.Word) bool {
	if word == nil {
		return true
	}
	return plainExpansionParts(word.Parts)
}

func plainExpansionParts(parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if p.Dollar || !plainExpansionParts(p.Parts) {
				return false
			}
		case *syntax.ParamExp:
			if p.Excl || p.Index != nil || p.Slice != nil || p.Repl != nil || p.Names != 0 || p.Exp != nil || p.Width {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// dbClientReadOnly reports whether every argument of a mysql or psql call is a
// connection setting, an output format flag or the inline SQL payload, so the
// call can neither read a script nor write an output, log or pager file.
func dbClientReadOnly(name string, args []string) bool {
	valueOptions := map[string]bool{"-h": true, "--host": true, "-P": true, "--port": true, "-u": true, "--user": true, "-D": true, "--database": true, "-S": true, "--socket": true, "--protocol": true, "--default-character-set": true, "-e": true, "--execute": true}
	flags := map[string]bool{"-N": true, "--skip-column-names": true, "-B": true, "--batch": true, "-s": true, "--silent": true, "-t": true, "--table": true, "-E": true, "--vertical": true, "-r": true, "--raw": true, "-A": true, "--no-auto-rehash": true, "-p": true, "--password": true}
	attached := "hPuDSpe"
	maxOperands := 1
	if name == "psql" {
		valueOptions = map[string]bool{"-h": true, "--host": true, "-p": true, "--port": true, "-U": true, "--username": true, "-d": true, "--dbname": true, "-c": true, "--command": true, "-F": true, "--field-separator": true, "-R": true, "--record-separator": true, "-v": true, "--set": true, "--variable": true}
		flags = map[string]bool{"-A": true, "--no-align": true, "-t": true, "--tuples-only": true, "-x": true, "--expanded": true, "-X": true, "--no-psqlrc": true, "-q": true, "--quiet": true, "-w": true, "--no-password": true, "-W": true, "--password": true, "--csv": true}
		attached = "hpUdcFRv"
		maxOperands = 2
	}
	operands := 0
	for index := 0; index < len(args); index++ {
		arg := args[index]
		option, _, hasValue := strings.Cut(arg, "=")
		switch {
		case flags[arg]:
		case valueOptions[arg]:
			index++
		case strings.HasPrefix(arg, "--") && hasValue && valueOptions[option]:
		case strings.HasPrefix(arg, "--ssl") && name != "psql":
		case len(arg) > 2 && arg[0] == '-' && arg[1] != '-' && strings.IndexByte(attached, arg[1]) >= 0:
		case !strings.HasPrefix(arg, "-"):
			operands++
			if operands > maxOperands {
				return false
			}
		default:
			return false
		}
	}
	return true
}
