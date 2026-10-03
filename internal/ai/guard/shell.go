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

type shellRedirect struct {
	op     string
	target string
}

type shellSegment struct {
	tokens    []string
	ops       []string
	redirects []shellRedirect
	raw       string
}

type stdinHint struct {
	text string
	ok   bool
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
		result = Worst(result, Dangerous("命令无法可靠解析: "+err.Error()))
	}
	for _, substitution := range substitutions {
		result = Worst(result, Confirm(KindUnknown, "包含命令替换"), classifyCommandDepth(substitution, rules, depth+1))
	}
	if len(segments) == 0 {
		return result
	}
	for index, segment := range segments {
		stdin := stdinHint{}
		if index > 0 && containsString(segments[index-1].ops, "|") {
			stdin = echoStdinHint(segments[index-1])
		}
		if len(segment.tokens) == 0 {
			if segment.hasWriteRedirection() {
				result = Worst(result, classifyRedirection(segment))
			}
			continue
		}
		result = Worst(result, classifySegment(segment, rules, depth, stdin))
		if result.Risk == Forbidden {
			return result
		}
	}
	return executionFloor(segments, result)
}

// executionFloor is the single conservative layer every classification
// passes through. A pipe into an execution consumer (shells, interpreters,
// at/batch/xargs — including through sudo, su, env and execution wrappers)
// whose input bytes are not exactly determinable from a provable producer
// (backslash-free echo, fully modeled printf) rules Danger: the consumer
// would execute content the guard cannot review.
func executionFloor(segments []shellSegment, result Ruling) Ruling {
	if result.Risk >= Danger {
		return result
	}
	for index := 1; index < len(segments); index++ {
		if !containsString(segments[index-1].ops, "|") {
			continue
		}
		tokens := stripDescriptors(segments[index].tokens)
		if len(tokens) == 0 {
			continue
		}
		consumer, resolved := floorConsumerName(tokens)
		executor := !resolved || isInterpreter(consumer) || consumer == "at" || consumer == "batch" || consumer == "xargs" || consumer == "su"
		if !executor {
			continue
		}
		if !echoStdinHint(segments[index-1]).ok {
			return Dangerous("管道输入无法完整确定，执行器可能运行未审查的内容")
		}
	}
	return result
}

// floorConsumerName resolves the command that ultimately executes, looking
// through env, privilege escalation and execution wrappers. resolved is
// false when the structure cannot be aligned; callers treat that as an
// execution consumer.
func floorConsumerName(tokens []string) (string, bool) {
	for len(tokens) > 0 {
		if isAssignment(tokens[0]) {
			tokens = tokens[1:]
			continue
		}
		if len(tokens) == 0 {
			return "", false
		}
		if commandName(tokens[0]) == "env" {
			rest := envNestedTokens(tokens[1:])
			if rest == nil {
				return "", false
			}
			tokens = rest
			continue
		}
		tokens = nestedExecutor(tokens)
		if len(tokens) == 0 {
			return "", false
		}
		return commandName(tokens[0]), true
	}
	return "", false
}

// envNestedTokens skips env options and assignments to reach the wrapped
// command. It returns nil when the -S/--split-string payload makes the
// wrapped command statically unresolvable.
func envNestedTokens(args []string) []string {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return args[index+1:]
		}
		if strings.HasPrefix(arg, "--") {
			option, _, attached, ok := resolveGNULongOption(arg, envLongOptions)
			if !ok {
				return nil
			}
			if option.name == "split-string" {
				return nil
			}
			if option.takesValue && !attached {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			letters := arg[1:]
			for len(letters) > 0 {
				switch letters[0] {
				case 'i', '0', 'v':
					letters = letters[1:]
				case 'u', 'C', 'a':
					if len(letters) == 1 {
						index++
					}
					letters = ""
				case 'S':
					return nil
				default:
					return nil
				}
			}
			continue
		}
		if isAssignment(arg) {
			continue
		}
		return args[index:]
	}
	return []string{}
}

func echoStdinHint(segment shellSegment) stdinHint {
	if len(segment.tokens) < 2 {
		return stdinHint{}
	}
	switch commandName(segment.tokens[0]) {
	case "echo":
		args := segment.tokens[1:]
		for len(args) > 0 && echoOption(args[0]) {
			args = args[1:]
		}
		// dash-family echo interprets backslash escapes by default and
		// -e/-E make bash do the same, so the emitted bytes are only
		// exactly determinable when no backslash is present.
		for _, arg := range args {
			if strings.ContainsRune(arg, '\\') {
				return stdinHint{}
			}
		}
		return stdinHint{text: strings.Join(args, " "), ok: true}
	case "printf":
		return printfStdinHint(segment.tokens[1:])
	}
	return stdinHint{}
}

func echoOption(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' {
		return false
	}
	for _, r := range arg[1:] {
		if r != 'n' && r != 'e' && r != 'E' {
			return false
		}
	}
	return true
}

// printfStdinHint models the bytes printf emits for a statically known
// format string and arguments so downstream consumers (at, xargs) classify
// the real payload instead of the format directives. Only the modeled
// escapes and verbs are accepted; anything else (%b, hex/octal escapes,
// unknown verbs) makes the output shell-dependent and fails closed
// (ok == false), which consumers rate Danger.
func printfStdinHint(args []string) stdinHint {
	if len(args) == 0 {
		return stdinHint{ok: true}
	}
	format := args[0]
	rest := args[1:]
	argIndex := 0
	takeArg := func() string {
		if argIndex < len(rest) {
			value := rest[argIndex]
			argIndex++
			return value
		}
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c == '\\' {
			if i+1 >= len(format) {
				return stdinHint{}
			}
			i++
			switch format[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case 'a':
				out.WriteByte('\a')
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case 'v':
				out.WriteByte('\v')
			case '\\':
				out.WriteByte('\\')
			case 'c':
				return stdinHint{text: out.String(), ok: true}
			default:
				return stdinHint{}
			}
			continue
		}
		if c != '%' {
			out.WriteByte(c)
			continue
		}
		if i+1 >= len(format) {
			return stdinHint{}
		}
		i++
		for i < len(format) && strings.ContainsRune("-+ #0.123456789", rune(format[i])) {
			i++
		}
		if i >= len(format) {
			return stdinHint{}
		}
		switch format[i] {
		case '%':
			out.WriteByte('%')
		case 's', 'c', 'd', 'i', 'o', 'u', 'x', 'X', 'f', 'e', 'E', 'g', 'G':
			out.WriteString(takeArg())
		default:
			return stdinHint{}
		}
	}
	for ; argIndex < len(rest); argIndex++ {
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(rest[argIndex])
	}
	return stdinHint{text: out.String(), ok: true}
}

func parseShell(input string) ([]shellSegment, []string, error) {
	var segments []shellSegment
	var current shellSegment
	var word strings.Builder
	var substitutions []string
	var quote rune
	escaped := false
	wordSeen := false
	redirectIndex := -1
	flushWord := func() {
		if wordSeen || word.Len() != 0 {
			value := word.String()
			current.tokens = append(current.tokens, value)
			if redirectIndex >= 0 {
				current.redirects[redirectIndex].target = value
				redirectIndex = -1
			}
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
		redirectIndex = -1
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
			if quote == '"' && r == '$' && i+1 < len(runes) && runes[i+1] == '(' {
				if i+2 < len(runes) && runes[i+2] == '(' {
					end, ok := readArithmetic(runes, i+3)
					if !ok {
						return segments, substitutions, strconv.ErrSyntax
					}
					wordSeen = true
					i = end
					continue
				}
				value, next, ok := readBalanced(runes, i+2)
				if !ok {
					return segments, substitutions, strconv.ErrSyntax
				}
				substitutions = append(substitutions, value)
				word.WriteString("$(" + value + ")")
				wordSeen = true
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
			if i+2 < len(runes) && runes[i+1] == '(' && runes[i+2] == '(' {
				end, ok := readArithmetic(runes, i+3)
				if !ok {
					return segments, substitutions, strconv.ErrSyntax
				}
				wordSeen = true
				i = end
			} else if i+1 < len(runes) && runes[i+1] == '(' {
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
			redirectIndex = -1
		case '=':
			if i+1 < len(runes) && runes[i+1] == '(' {
				value, next, ok := readBalanced(runes, i+2)
				if !ok {
					return segments, substitutions, strconv.ErrSyntax
				}
				substitutions = append(substitutions, value)
				word.WriteString("=(" + value + ")")
				wordSeen = true
				i = next
				continue
			}
			word.WriteRune(r)
			wordSeen = true
		case '>', '<':
			if i+1 < len(runes) && runes[i+1] == '(' {
				value, next, ok := readBalanced(runes, i+2)
				if !ok {
					return segments, substitutions, strconv.ErrSyntax
				}
				substitutions = append(substitutions, value)
				word.WriteString(string(r) + "(" + value + ")")
				wordSeen = true
				i = next
				continue
			}
			flushWord()
			op := string(r)
			if i+1 < len(runes) && (runes[i+1] == r || r == '>' && runes[i+1] == '&') {
				i++
				current.raw += string(runes[i])
				op += string(runes[i])
			}
			current.ops = append(current.ops, op)
			current.redirects = append(current.redirects, shellRedirect{op: op})
			redirectIndex = len(current.redirects) - 1
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

func readArithmetic(runes []rune, start int) (int, bool) {
	depth := 0
	for i := start; i+1 < len(runes); i++ {
		switch runes[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 && runes[i+1] == ')' {
				return i + 1, true
			}
			if depth > 0 {
				depth--
			}
		}
	}
	return start, false
}

func (s shellSegment) hasWriteRedirection() bool {
	writes := 0
	target := ""
	for _, redirect := range s.redirects {
		if !strings.HasPrefix(redirect.op, ">") {
			continue
		}
		_, numeric := strconv.Atoi(redirect.target)
		if redirect.op == ">&" && (redirect.target == "-" || numeric == nil) {
			continue
		}
		writes++
		target = redirect.target
	}
	if writes == 1 && target == "/dev/null" {
		return false
	}
	return writes != 0
}

func classifyRedirection(segment shellSegment) Ruling {
	for _, redirect := range segment.redirects {
		if strings.HasPrefix(redirect.op, ">") {
			if isCriticalWriteTarget(redirect.target) {
				return Deny("禁止写入关键系统路径")
			}
			return Confirm(KindWriteFS, "包含文件重定向")
		}
	}
	return Confirm(KindUnknown, "包含无法确认安全性的 shell 操作")
}

func classifySegment(segment shellSegment, rules []string, depth int, stdin stdinHint) Ruling {
	if depth > 8 {
		return Dangerous("命令嵌套过深，无法安全分析")
	}
	tokens := stripDescriptors(segment.tokens)
	for len(tokens) > 0 && isAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return Allow()
	}
	for _, token := range tokens {
		if strings.Contains(token, "/dev/tcp/") || strings.Contains(token, "/dev/udp/") {
			return Dangerous("重定向到网络设备")
		}
	}
	result := Allow()
	if segment.hasWriteRedirection() {
		for _, redirect := range segment.redirects {
			if strings.HasPrefix(redirect.op, ">") && isCriticalWriteTarget(redirect.target) {
				return Deny("禁止写入关键系统路径")
			}
		}
		result = Confirm(KindWriteFS, "包含文件重定向")
		for i, token := range tokens {
			if isBlockDevice(token) && i > 0 {
				return Deny("禁止直接写入块设备")
			}
		}
	}
	for len(tokens) > 0 {
		name := commandName(tokens[0])
		if name == "sudo" || name == "doas" || name == "sudoedit" {
			result = Worst(result, Confirm(KindSudo, "包含权限提升"))
			if name == "sudoedit" || name == "sudo" && sudoEditFlag(tokens[1:]) {
				for _, target := range stripCommandFlags(tokens[1:]) {
					if isCriticalWriteTarget(target) {
						return Deny("禁止以 root 编辑关键系统路径")
					}
				}
				return Worst(result, Dangerous("以 root 编辑文件"))
			}
			tokens = stripCommandFlags(tokens[1:])
			continue
		}
		if name == "su" {
			result = Worst(result, Confirm(KindSudo, "包含身份切换"))
			for index := 1; index < len(tokens); index++ {
				arg := tokens[index]
				if arg == "--" {
					break
				}
				if strings.HasPrefix(arg, "--") {
					option, value, attached, ok := resolveGNULongOption(arg, suLongOptions)
					if !ok {
						continue
					}
					switch option.name {
					case "command", "session-command":
						if !attached && index+1 < len(tokens) {
							index++
							value = tokens[index]
						}
						result = Worst(result, classifyCommandDepth(value, rules, depth+1))
					default:
						if option.takesValue && !attached {
							index++
						}
					}
					continue
				}
				if strings.HasPrefix(arg, "-") && len(arg) > 1 {
					letters := arg[1:]
					for len(letters) > 0 {
						letter := letters[0]
						switch {
						case letter == 'c':
							code := letters[1:]
							if code == "" && index+1 < len(tokens) {
								index++
								code = tokens[index]
							}
							if code != "" {
								result = Worst(result, classifyCommandDepth(code, rules, depth+1))
							}
							letters = ""
						case letter == 'l' || letter == 'm' || letter == 'p':
							letters = letters[1:]
						case letter == 's' || letter == 'g' || letter == 'G' || letter == 'w':
							if len(letters) == 1 {
								index++
							}
							letters = ""
						default:
							letters = letters[1:]
						}
					}
				}
			}
			return result
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
	return Worst(result, classifySimple(tokens, segment.ops, rules, depth, stdin))
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
	valueFlags := map[string]bool{
		"-u": true, "-g": true, "-h": true, "-p": true, "-a": true, "-C": true, "-D": true, "-T": true, "-t": true, "-U": true, "-G": true, "-R": true,
		"--user": true, "--group": true, "--host": true, "--prompt": true, "--chdir": true, "--command-timeout": true, "--type": true, "--role": true,
	}
	for len(tokens) > 0 {
		arg := tokens[0]
		if arg == "--" {
			return tokens[1:]
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
		tokens = tokens[1:]
		if valueFlags[arg] && len(tokens) > 0 {
			tokens = tokens[1:]
			continue
		}
		if value, ok := privilegeOptionValue(arg); ok && value == "" && len(tokens) > 0 {
			tokens = tokens[1:]
		}
	}
	return tokens
}

func privilegeOptionValue(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return "", false
	}
	for index := 1; index < len(arg); index++ {
		if strings.IndexByte(sudoValueShorts, arg[index]) >= 0 {
			return arg[index+1:], true
		}
	}
	return "", false
}

// sudoEditFlag reports whether leading sudo options select edit mode. It
// understands clustered short options (sudo -eu root means -e -u root),
// attached forms, and GNU long-option abbreviations; scanning stops at the
// wrapped command so command flags are never mistaken for sudo flags.
func sudoEditFlag(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			option, _, attached, ok := resolveGNULongOption(arg, sudoLongOptions)
			if !ok {
				continue
			}
			if option.name == "edit" {
				return true
			}
			if option.takesValue && !attached {
				index++
			}
			continue
		}
		letters := arg[1:]
		for len(letters) > 0 {
			letter := letters[0]
			if letter == 'e' {
				return true
			}
			if strings.IndexByte(sudoValueShorts, letter) >= 0 {
				if len(letters) == 1 {
					index++
				}
				letters = ""
				continue
			}
			letters = letters[1:]
		}
	}
	return false
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

func classifySimple(tokens []string, ops []string, rules []string, depth int, stdin stdinHint) Ruling {
	name := commandName(tokens[0])
	args := tokens[1:]
	if isInterpreter(name) {
		result := Worst(Confirm(KindUnknown, "解释器或脚本执行需要确认"), classifyInterpreter(name, args, rules, depth))
		if stdin.ok && strings.TrimSpace(stdin.text) != "" {
			if isShellInterpreter(name) {
				result = Worst(result, classifyCommandDepth(stdin.text, rules, depth+1))
			} else {
				result = Worst(result, Dangerous(name+" 从标准输入执行代码"))
			}
		}
		return result
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
	if ruling, ok := classifyStateChangingBuiltins(name, args, rules, depth, stdin); ok {
		return ruling
	}
	if wrapper, ok := execWrappers[name]; ok {
		return classifyWrapper(name, wrapper, args, rules, depth, stdin)
	}
	switch name {
	case "rm", "rmdir", "mv", "cp", "dd", "mkfs", "mkfs.ext4", "mkfs.xfs", "shred", "truncate":
		if writesCriticalTarget(name, args) {
			return Deny("禁止写入关键系统路径")
		}
		return Confirm(KindWriteFS, "文件系统写操作")
	case "touch", "mkdir", "install", "ln", "unlink", "tee":
		if writesCriticalTarget(name, args) {
			return Deny("禁止写入关键系统路径")
		}
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
		return Dangerous("进程终止")
	case "chmod", "chown", "chgrp", "chattr", "setfacl":
		return Confirm(KindPermission, "权限或账户变更")
	case "useradd", "userdel", "usermod", "passwd":
		return Dangerous("账户变更")
	case "crontab":
		return classifyCrontab(args)
	case "docker", "podman":
		return classifyDocker(args, rules, depth)
	case "kubectl":
		return classifyKubectl(args, rules, depth)
	case "git":
		return classifyGit(args, rules, depth)
	case "redis-cli", "valkey-cli":
		return classifyRedis(args)
	case "mysql", "mariadb", "psql":
		return classifyDatabaseClient(name, args, rules, ops, depth)
	case "sed", "awk", "gawk", "ed", "vim", "vi", "nano", "emacs":
		return Confirm(KindUnknown, "命令具有编辑或执行子命令能力")
	case "find":
		return classifyFind(args, rules, depth)
	case "tar":
		for _, arg := range args {
			lower := strings.ToLower(arg)
			if lower == "--to-command" || lower == "--use-compress-program" || lower == "--checkpoint-action" || strings.HasPrefix(lower, "--checkpoint-action=") || strings.HasPrefix(lower, "--use-compress-program=") {
				return Confirm(KindUnknown, "tar 包含外部命令执行")
			}
		}
		for index, arg := range args {
			if arg == "-C" || arg == "--directory" {
				if index+1 < len(args) && isCriticalRoot(args[index+1]) {
					return Dangerous("tar 解包到关键路径")
				}
			}
			if strings.HasPrefix(arg, "--directory=") && isCriticalRoot(strings.TrimPrefix(arg, "--directory=")) {
				return Dangerous("tar 解包到关键路径")
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
	case "nc", "ncat", "netcat", "socat":
		for _, arg := range args {
			lower := strings.ToLower(arg)
			if lower == "--exec" || strings.HasPrefix(lower, "--exec=") || lower == "--sh-exec" || strings.HasPrefix(lower, "--sh-exec=") || lower == "--lua-exec" || strings.HasPrefix(lower, "--lua-exec=") {
				return Dangerous("网络工具执行命令")
			}
			if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg[1:], "ec") {
				// -e/-c execute a command; attached forms (-e/bin/sh) and
				// clusters are execution capability just the same.
				return Dangerous("网络工具执行命令")
			}
			if strings.HasPrefix(lower, "exec:") || strings.HasPrefix(lower, "system:") || strings.HasPrefix(lower, "shell") {
				return Dangerous("网络工具执行命令")
			}
		}
		return Confirm(KindUnknown, "网络工具需要确认")
	case "ssh":
		return classifySSH(args, rules, depth)
	case "scp", "sftp", "rsync":
		return Confirm(KindUnknown, "远程传输或执行需要确认")
	}
	if safeFirstToken[name] {
		return Allow()
	}
	return Confirm(KindUnknown, "未列入只读白名单的命令")
}

var safeFirstToken = func() map[string]bool {
	values := strings.Fields(`ls ll pwd cat head tail tac nl grep egrep fgrep rg cut sort uniq wc stat file tree diff comm jq xxd hexdump od strings ps pidof df du free vmstat iostat lscpu lsblk uname whoami id hostname hostnamectl uptime date cal env printenv which whereis type echo printf dmesg journalctl ss netstat ip ifconfig arp route ping traceroute dig nslookup host lsof top htop atop sar w who last history md5sum sha1sum sha256sum zcat zipinfo basename dirname readlink realpath getent ulimit tput resize lsb_release arch nproc true false sleep clear select show describe desc explain cd pushd popd tr base64 base32 seq shuf export unset readonly`)
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

func isShellInterpreter(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "ksh", "dash", "fish":
		return true
	default:
		return false
	}
}

// interpreterCodeLetters lists the short options through which non-shell
// interpreters accept inline source (-c/-e/-E/-r/-p, attached or separate).
const interpreterCodeLetters = "ceErp"

func classifyInterpreter(name string, args []string, rules []string, depth int) Ruling {
	result := Allow()
	shell := isShellInterpreter(name)
	inlineCode := ""
	inline := false
	takeCode := func(code string) {
		inline = true
		if shell {
			result = Worst(result, classifyCommandDepth(code, rules, depth+1))
		} else {
			inlineCode += " " + code
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if shell {
			switch {
			case arg == "-c":
				if i+1 < len(args) {
					takeCode(args[i+1])
				}
			case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.IndexByte(arg[1:], 'c') >= 0:
				// Clustered -c. getopt-style shells (dash) treat the rest
				// of the cluster as the attached script, while bash/zsh
				// treat them as more flags and take the next argument.
				// Classify both interpretations and keep the worse ruling.
				position := strings.IndexByte(arg[1:], 'c')
				if remainder := arg[position+2:]; remainder != "" {
					takeCode(remainder)
				}
				if i+1 < len(args) {
					takeCode(args[i+1])
				}
			}
			continue
		}
		lower := strings.ToLower(arg)
		switch {
		case arg == "-c" || arg == "-e" || arg == "-E" || arg == "-r" || arg == "-p" || strings.EqualFold(arg, "-Command") || arg == "/c" || arg == "--eval" || arg == "--command" || arg == "--execute":
			if i+1 < len(args) {
				takeCode(args[i+1])
			}
		case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--"):
			letters := arg[1:]
			for len(letters) > 0 {
				if strings.IndexByte(interpreterCodeLetters, letters[0]) >= 0 {
					takeCode(letters[1:])
					letters = ""
					continue
				}
				letters = letters[1:]
			}
		case strings.HasPrefix(lower, "--eval=") || strings.HasPrefix(lower, "--command=") || strings.HasPrefix(lower, "--execute="):
			takeCode(arg[strings.Index(arg, "=")+1:])
		}
	}
	if inline && !shell {
		result = Worst(result, Dangerous("解释器内联执行任意代码"), dangerousInterpreterText(inlineCode, rules))
	}
	return result
}

func dangerousInterpreterText(value string, rules []string) Ruling {
	if matchDangerRule(value, rules) || containsDestructiveText(value) {
		return Dangerous("解释器代码包含危险操作")
	}
	return Allow()
}

func classifyDatabaseClient(name string, args, rules []string, ops []string, depth int) Ruling {
	result := Confirm(KindDBWrite, "交互式数据库客户端需要确认")
	for _, op := range ops {
		if op == "<" {
			return Dangerous("数据库客户端执行重定向 SQL 文件")
		}
	}
	for i, arg := range args {
		if name == "psql" && (arg == "-f" || arg == "--file") {
			return Dangerous("psql 执行 SQL 文件，内容无法验证")
		}
		payload := ""
		switch {
		case (arg == "-e" && name != "psql") || (arg == "-c" && name == "psql") || arg == "--execute" || arg == "--command":
			if i+1 < len(args) {
				payload = args[i+1]
			}
		case arg == "--init-command" && name != "psql":
			if i+1 < len(args) {
				payload = args[i+1]
			}
		case strings.HasPrefix(arg, "--execute="):
			payload = strings.TrimPrefix(arg, "--execute=")
		case strings.HasPrefix(arg, "--command="):
			payload = strings.TrimPrefix(arg, "--command=")
		case name != "psql" && strings.HasPrefix(arg, "--init-command="):
			payload = strings.TrimPrefix(arg, "--init-command=")
		case name != "psql" && strings.HasPrefix(arg, "-e") && len(arg) > 2:
			payload = arg[2:]
		case name == "psql" && strings.HasPrefix(arg, "-c") && len(arg) > 2:
			payload = arg[2:]
		}
		if payload == "" {
			flag := byte('e')
			if name == "psql" {
				flag = 'c'
			}
			if value, ok := shortOptionValue(arg, flag); ok {
				if value != "" {
					payload = value
				} else if i+1 < len(args) {
					payload = args[i+1]
				}
			}
		}
		if payload != "" {
			result = Worst(result, classifyDatabaseMetaCommand(name, payload, rules, depth))
			result = Worst(result, ClassifySQL(payload, rules))
		}
	}
	return result
}

// classifyDatabaseMetaCommand rates client-level escapes that execute files
// or shell commands. MySQL splits -e input at statement boundaries, so
// every statement is checked for source/\. and system; psql takes a single
// command per -c, so only its leading escape executes.
func classifyDatabaseMetaCommand(name, payload string, rules []string, depth int) Ruling {
	if name == "psql" {
		trimmed := strings.TrimSpace(payload)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(trimmed, `\!`):
			return Worst(Dangerous("psql 执行 shell 转义命令"), classifyCommandDepth(strings.TrimSpace(trimmed[2:]), rules, depth+1))
		case strings.HasPrefix(lower, `\i`) || strings.HasPrefix(lower, `\include`):
			return Dangerous("psql 执行 SQL 文件，内容无法验证")
		}
		return Allow()
	}
	for _, statement := range strings.Split(payload, ";") {
		trimmed := strings.TrimSpace(statement)
		lower := strings.ToLower(trimmed)
		switch {
		case lower == "source" || strings.HasPrefix(lower, "source ") || strings.HasPrefix(trimmed, `\.`):
			return Dangerous("mysql 执行 SQL 文件，内容无法验证")
		case lower == "system" || strings.HasPrefix(lower, "system "):
			return Worst(Dangerous("mysql 执行 shell 转义命令"), classifyCommandDepth(strings.TrimSpace(trimmed[len("system"):]), rules, depth+1))
		}
	}
	return Allow()
}

func containsDestructiveText(value string) bool {
	lower := strings.ToLower(strings.ReplaceAll(value, " ", ""))
	for _, text := range []string{"rm-rf/", "remove-item-force", "remove-item-recurse", "rmtree(\"/", "rmsync(\"/", "dropdatabase", "flushall"} {
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
	if name == "chmod" && containsAny(args, "-R", "--recursive") && containsAny(args, "777", "0777", "a+rwx", "ugo+rwx", "7777") {
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
	if strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "$HOME/") || strings.HasPrefix(value, "${HOME}/") {
		return true
	}
	if strings.HasPrefix(value, "~") && !strings.Contains(value, "/") {
		return true
	}
	normalized := strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(normalized, "c:/users") || normalized == "c:" || normalized == "c:/" {
		return true
	}
	if strings.HasPrefix(value, "/") {
		value = filepath.Clean(value)
	}
	switch value {
	case "/", "/*", "/~", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/home", "/sbin", "/sys", "/usr", "/var", "/Users", "C:", "c:":
		return true
	default:
		return strings.HasPrefix(value, "/home/") || strings.HasPrefix(value, "/root/") || strings.HasPrefix(value, "/Users/")
	}
}

func isCriticalWriteTarget(value string) bool {
	target := strings.ToLower(strings.ReplaceAll(strings.Trim(strings.TrimSpace(value), "'\""), "\\", "/"))
	if target == "" {
		return false
	}
	if strings.HasPrefix(target, "/") {
		target = filepath.Clean(target)
	}
	for _, prefix := range []string{
		"/etc/cron", "/var/spool/cron", "/etc/sudoers", "/etc/shadow", "/etc/passwd", "/etc/ssh", "/etc/ld.so.preload",
		"/etc/systemd/system", "/etc/profile", "/etc/bash.bashrc", "/etc/zsh",
	} {
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return strings.Contains(target, "authorized_keys") || strings.Contains(target, "/.ssh/")
}

func isBlockDevice(value string) bool {
	value = filepath.Clean(strings.Trim(value, "'\""))
	for _, prefix := range []string{"/dev/sd", "/dev/hd", "/dev/nvme", "/dev/mapper/", "/dev/vd", "/dev/xvd", "/dev/disk", "/dev/rdisk"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// writesCriticalTarget reports whether a file-producing command writes to a
// critical system path. It shares the canonicalized target model with
// redirection and sudo-edit protection.
func writesCriticalTarget(name string, args []string) bool {
	valueFlags := map[string]bool{
		"-t": true, "--target-directory": true, "-s": true, "--size": true, "-n": true,
		"-m": true, "-o": true, "-g": true, "-d": true, "-S": true, "--suffix": true,
	}
	var positionals []string
	skip := false
	for _, arg := range args {
		if skip {
			skip = false
			continue
		}
		if arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if valueFlags[arg] {
				skip = true
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	var targets []string
	switch name {
	case "dd":
		for _, arg := range args {
			if strings.HasPrefix(arg, "of=") {
				targets = append(targets, strings.TrimPrefix(arg, "of="))
			}
		}
	case "cp", "mv", "install", "ln":
		if len(positionals) > 0 {
			targets = append(targets, positionals[len(positionals)-1])
		}
		for index, arg := range args {
			if (arg == "-t" || arg == "--target-directory") && index+1 < len(args) {
				targets = append(targets, args[index+1])
			}
			if strings.HasPrefix(arg, "--target-directory=") {
				targets = append(targets, strings.TrimPrefix(arg, "--target-directory="))
			}
		}
	case "rm", "rmdir", "unlink", "tee", "truncate", "shred":
		targets = append(targets, positionals...)
	default:
		return false
	}
	for _, target := range targets {
		if isCriticalWriteTarget(target) {
			return true
		}
	}
	return false
}

func classifyFind(args []string, rules []string, depth int) Ruling {
	result := Allow()
	writeAction := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "-delete":
			return Dangerous("find -delete 递归删除")
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			writeAction = true
		case "-exec", "-execdir", "-ok", "-okdir":
			writeAction = true
			end := len(args)
			for scan := index + 1; scan < len(args); scan++ {
				if args[scan] == ";" || args[scan] == "+" {
					end = scan
					break
				}
			}
			if index+1 < end {
				result = Worst(result, Dangerous("find 执行外部命令"), classifyCommandDepth(strings.Join(args[index+1:end], " "), rules, depth+1))
			}
			index = end
		}
	}
	if writeAction {
		return Worst(result, Confirm(KindWriteFS, "find 包含写入或执行动作"))
	}
	return result
}

func classifyCrontab(args []string) Ruling {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-l":
			return Allow()
		case "-u", "-s":
			index++
		case "-i":
		case "-r", "-e":
			return Dangerous("crontab 变更计划任务")
		default:
			return Dangerous("crontab 安装计划任务")
		}
	}
	return Dangerous("crontab 从标准输入安装计划任务")
}

func classifySSH(args []string, rules []string, depth int) Ruling {
	valueOptions := map[string]bool{"-p": true, "-i": true, "-l": true, "-E": true, "-F": true, "-J": true, "-L": true, "-W": true, "-b": true, "-c": true, "-m": true, "-S": true}
	result := Confirm(KindUnknown, "远程传输或执行需要确认")
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		index++
		if arg == "-o" {
			if index < len(args) {
				result = Worst(result, classifySSHConfigValue(args[index], rules, depth))
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-o") && len(arg) > 2 {
			result = Worst(result, classifySSHConfigValue(arg[2:], rules, depth))
			continue
		}
		if valueOptions[arg] && index < len(args) {
			index++
		}
	}
	if index >= len(args) {
		return result
	}
	index++
	if index >= len(args) {
		return result
	}
	// ssh flattens the remote command into a single string for the remote
	// shell, so classification re-parses the joined text.
	return Worst(result, classifyCommandDepth(strings.Join(args[index:], " "), rules, depth+1))
}

// classifySSHConfigValue inspects -o values whose keywords run a command:
// ProxyCommand and LocalCommand execute locally through a shell, and
// RemoteCommand executes on the remote host.
func classifySSHConfigValue(value string, rules []string, depth int) Ruling {
	key, command, found := strings.Cut(value, "=")
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "proxycommand", "localcommand", "remotecommand":
		command = strings.TrimSpace(command)
		if !found || command == "" || strings.EqualFold(command, "none") {
			return Allow()
		}
		return Worst(Dangerous("ssh 选项执行本地或远端命令"), classifyCommandDepth(command, rules, depth+1))
	}
	return Allow()
}

func classifyService(name string, args []string) Ruling {
	if name == "systemctl" {
		index := 0
		sawFailed := false
		for index < len(args) && strings.HasPrefix(args[index], "-") {
			arg := args[index]
			if arg == "--" {
				index++
				break
			}
			if strings.HasPrefix(arg, "--") {
				option, _, attached, ok := resolveGNULongOption(arg, systemctlLongOptions)
				if !ok {
					return Dangerous("systemctl 包含无法识别的全局选项")
				}
				index++
				if option.name == "failed" {
					sawFailed = true
				}
				if option.takesValue && !attached {
					index++
				}
				continue
			}
			letters := arg[1:]
			for len(letters) > 0 {
				switch letters[0] {
				case 'H', 'M', 't', 'o':
					if len(letters) == 1 {
						index++
					}
					letters = ""
				default:
					return Dangerous("systemctl 包含无法识别的全局选项")
				}
			}
			index++
		}
		if index >= len(args) {
			if sawFailed {
				return Allow()
			}
			return Confirm(KindService, "服务或防火墙配置变更")
		}
		switch args[index] {
		case "status", "show", "list-units", "list-unit-files", "is-active", "is-enabled", "list-dependencies", "cat":
			return Allow()
		case "stop", "mask", "kill", "isolate", "poweroff", "reboot", "halt":
			return Dangerous("系统服务停止或隔离")
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

func dockerGlobalEnd(args []string) int {
	valueOptions := map[string]bool{"-H": true, "--host": true, "--config": true, "--context": true, "--log-level": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true}
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		index++
		if valueOptions[arg] && index < len(args) {
			index++
		}
	}
	return index
}

func classifyDocker(args []string, rules []string, depth int) Ruling {
	if len(args) == 0 {
		return Confirm(KindUnknown, "docker 子命令不明确")
	}
	index := dockerGlobalEnd(args)
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
		if commandIndex := dockerExecCommandIndex(rest); commandIndex >= 0 {
			result = Worst(result, classifySegment(shellSegment{tokens: rest[commandIndex:]}, rules, depth, stdinHint{}))
		}
		return result
	case "run", "create", "start", "stop", "restart", "rm", "kill", "pause", "unpause", "rename", "update", "cp", "commit", "import", "load", "pull", "push", "build", "tag", "rmi", "save", "attach":
		if (sub == "rm" || sub == "rmi") && dockerForceFlag(rest) && dockerBulkDeletion(rest) {
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
		case "ls", "inspect", "ps", "config", "logs", "version", "df", "top", "history":
			return Allow()
		case "prune":
			return Dangerous("Docker 清理会删除资源")
		case "exec", "run":
			return classifyDocker(append([]string{nested}, rest[1:]...), rules, depth)
		case "rm", "rmi":
			if dockerForceFlag(rest[1:]) && dockerBulkDeletion(rest[1:]) {
				return Dangerous("强制删除全部 Docker 资源")
			}
			return Confirm(KindDockerMutate, "Docker 资源变更")
		default:
			return Confirm(KindDockerMutate, "Docker 资源变更")
		}
	default:
		return Confirm(KindUnknown, "未知 Docker 子命令")
	}
}

func dockerExecCommandIndex(args []string) int {
	valueOptions := map[string]bool{
		"-e": true, "--env": true, "--env-file": true, "-u": true, "--user": true, "-w": true, "--workdir": true, "--detach-keys": true,
	}
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		index++
		if arg == "--" {
			break
		}
		if valueOptions[arg] && index < len(args) {
			index++
		}
	}
	if index >= len(args) {
		return -1
	}
	index++
	if index >= len(args) {
		return -1
	}
	return index
}

func dockerForceFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--force" || strings.HasPrefix(arg, "--force=") {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'f') {
			return true
		}
	}
	return false
}

// substitutionTokens tokenizes a "$(...)" payload with the shell parser so
// quoted arguments keep their boundaries; multi-segment payloads are not
// statically alignable and report false.
func substitutionTokens(payload string) ([]string, bool) {
	segments, _, err := parseShell(payload)
	if err != nil || len(segments) != 1 {
		return nil, false
	}
	return segments[0].tokens, true
}

func dockerBulkDeletion(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "$(") || !strings.HasSuffix(arg, ")") {
			continue
		}
		fields, ok := substitutionTokens(arg[2 : len(arg)-1])
		if !ok {
			continue
		}
		// Look through privilege escalation and execution wrappers (sudo,
		// nice, command, env, ...) to the docker/podman enumeration itself.
		fields = nestedExecutor(fields)
		if len(fields) == 0 || commandName(fields[0]) != "docker" && commandName(fields[0]) != "podman" {
			continue
		}
		rest := fields[1:]
		offset := dockerGlobalEnd(rest)
		if offset >= len(rest) {
			continue
		}
		enumeration := ""
		switch sub := strings.ToLower(rest[offset]); sub {
		case "ps", "images":
			enumeration = sub
		case "container", "image":
			if offset+1 < len(rest) {
				nested := strings.ToLower(rest[offset+1])
				if sub == "container" && (nested == "ps" || nested == "ls" || nested == "list") {
					enumeration = "ps"
					offset++
				}
				if sub == "image" && (nested == "ls" || nested == "list") {
					enumeration = "images"
					offset++
				}
			}
		}
		if enumeration == "" {
			continue
		}
		quiet := false
		formatted := false
		for _, option := range rest[offset+1:] {
			if option == "--quiet" || option == "--quiet=true" {
				quiet = true
			}
			if option == "--format" || strings.HasPrefix(option, "--format=") {
				formatted = true
			}
			if _, ok := shortOptionValue(option, 'q'); ok {
				quiet = true
			}
		}
		// Force-removing every enumerated container or image (running set
		// included) is bulk destruction; quiet and --format enumerations
		// produce exactly the IDs the deletion consumes, and filters cannot
		// be verified statically. A plain table listing is not an ID source.
		if quiet || formatted {
			return true
		}
	}
	return false
}

func classifyKubectl(args []string, rules []string, depth int) Ruling {
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
		case "exec":
			index := 1
			for index < len(args) && strings.HasPrefix(args[index], "-") {
				arg := args[index]
				if arg == "--" {
					index++
					break
				}
				if strings.HasPrefix(arg, "--") {
					option, _, attached, ok := resolveGNULongOption(arg, kubectlExecLongOptions)
					if !ok {
						return Dangerous("kubectl exec 包含无法识别的选项")
					}
					index++
					if option.takesValue && !attached {
						index++
					}
					continue
				}
				letters := arg[1:]
				for len(letters) > 0 {
					switch letters[0] {
					case 'c', 'n':
						if len(letters) == 1 {
							index++
						}
						letters = ""
					case 'i', 't', 'q':
						letters = letters[1:]
					default:
						return Dangerous("kubectl exec 包含无法识别的选项")
					}
				}
				index++
			}
			if index < len(args) {
				index++
			}
			if index < len(args) && args[index] == "--" {
				index++
			}
			if index < len(args) {
				// kubectl exec passes the command argv to the container
				// process intact; classify the token slice, not a re-joined
				// string, so quoted scripts keep their boundaries.
				return Worst(Confirm(KindService, "Kubernetes 资源或工作负载变更"), classifySegment(shellSegment{tokens: args[index:]}, rules, depth+1, stdinHint{}))
			}
			return Confirm(KindService, "Kubernetes 资源或工作负载变更")
		case "delete", "drain", "cordon", "uncordon", "apply", "create", "replace", "patch", "scale", "rollout", "port-forward", "cp", "edit", "set", "label", "annotate", "taint":
			return Confirm(KindService, "Kubernetes 资源或工作负载变更")
		}
	}
	return Confirm(KindUnknown, "未知 kubectl 子命令")
}

func classifyGit(args []string, rules []string, depth int) Ruling {
	if len(args) == 0 {
		return Allow()
	}
	index := 0
	for index < len(args) {
		arg := args[index]
		if arg == "-C" || arg == "-c" {
			if index+1 < len(args) {
				if arg == "-c" {
					if ruling, bad := gitConfigInjection(args[index+1], rules, depth); bad {
						return ruling
					}
				}
				index += 2
				continue
			}
			index++
			continue
		}
		if strings.HasPrefix(arg, "-C") && len(arg) > 2 {
			index++
			continue
		}
		if strings.HasPrefix(arg, "-c") && len(arg) > 2 {
			if ruling, bad := gitConfigInjection(arg[2:], rules, depth); bad {
				return ruling
			}
			index++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg[2:], "=")
			if name == "exec-path" {
				// Subcommands are executed from this directory, so it
				// redirects execution regardless of the subcommand.
				return Dangerous("git --exec-path 重定向子命令执行")
			}
			if name == "config-env" {
				// The config value comes from an environment variable, so
				// executable keys cannot be verified statically.
				if !attached {
					index++
					if index < len(args) {
						value = args[index]
					}
				}
				if ruling, bad := gitConfigInjection(value, rules, depth); bad {
					return ruling
				}
				index++
				continue
			}
			if containsString(gitValueGlobals, name) {
				if !attached {
					index++
				}
				index++
				continue
			}
			if containsString(gitFlagGlobals, name) {
				index++
				continue
			}
			return Dangerous("git 包含无法识别的全局选项")
		}
		if strings.HasPrefix(arg, "-") {
			if containsString(gitFlagGlobals, arg) {
				index++
				continue
			}
			return Dangerous("git 包含无法识别的全局选项")
		}
		break
	}
	rest := args[index:]
	if len(rest) == 0 {
		return Allow()
	}
	for _, arg := range rest[1:] {
		lower := strings.ToLower(arg)
		if lower == "--output" || lower == "-o" || lower == "--exec" || strings.HasPrefix(lower, "--output=") || strings.HasPrefix(lower, "--exec=") || strings.HasPrefix(lower, "-o") && len(lower) > 2 {
			return Confirm(KindWriteFS, "Git 输出文件或扩展命令")
		}
	}
	switch rest[0] {
	case "status", "log", "diff", "show", "rev-parse", "ls-files", "blame", "shortlog", "describe":
		return Allow()
	case "branch":
		if len(rest) == 1 {
			return Allow()
		}
		for _, arg := range rest[1:] {
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
		if len(rest) == 1 || len(rest) == 2 && (rest[1] == "-v" || rest[1] == "--verbose" || rest[1] == "show" || rest[1] == "get-url") {
			return Allow()
		}
		return Confirm(KindWriteFS, "Git 远端配置变更")
	case "push":
		if containsAnyFold(rest[1:], "--force", "-f", "--force-with-lease") {
			return Confirm(KindWriteFS, "Git 强制推送")
		}
		return Confirm(KindWriteFS, "Git 推送")
	case "clean":
		return classifyGitClean(rest[1:])
	case "reset":
		if containsAnyFold(rest[1:], "--hard") {
			return Dangerous("git reset --hard 丢弃提交与修改")
		}
		return Confirm(KindWriteFS, "Git 仓库或远端变更")
	case "checkout", "switch", "restore", "rm", "mv", "commit", "merge", "rebase", "cherry-pick", "revert", "am", "apply", "config", "clone", "fetch", "pull", "submodule", "worktree", "stash", "tag", "add":
		return Confirm(KindWriteFS, "Git 仓库或远端变更")
	default:
		return Confirm(KindUnknown, "未知 Git 子命令")
	}
}

var gitValueGlobals = []string{"git-dir", "work-tree", "config-env", "namespace"}

var gitFlagGlobals = []string{"-p", "-P", "--paginate", "--no-pager", "--bare", "--version", "--help", "--no-replace-objects", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-optional-locks"}

// classifyGitClean parses clean options with getopt semantics: -e/--exclude
// consumes a value (so `git clean -fe -n` is NOT a dry run), and only a free
// -n/--dry-run makes the read-only dry run.
func classifyGitClean(args []string) Ruling {
	dryRun := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg[2:], "=")
			switch name {
			case "dry-run":
				dryRun = true
			case "exclude":
				if !attached {
					index++
				}
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			letters := arg[1:]
			for len(letters) > 0 {
				switch letters[0] {
				case 'n':
					dryRun = true
					letters = letters[1:]
				case 'e':
					if len(letters) == 1 {
						index++
					}
					letters = ""
				default:
					letters = letters[1:]
				}
			}
		}
	}
	if dryRun {
		return Allow()
	}
	return Dangerous("git clean 删除未跟踪文件")
}

// gitConfigInjection rates git -c values that configure an executable hook
// (pager, editor, ssh command, proxy, fsmonitor, external diff, filters,
// shell aliases) or import configuration indirectly (include.*): the value
// runs a command, so it is classified like one.
func gitConfigInjection(value string, rules []string, depth int) (Ruling, bool) {
	key, command, found := strings.Cut(value, "=")
	lower := strings.ToLower(key)
	executable := strings.Contains(lower, "pager") || strings.Contains(lower, "editor") || strings.Contains(lower, "sshcommand") || strings.Contains(lower, "proxy") ||
		strings.Contains(lower, "hookspath") || strings.Contains(lower, "external") || strings.Contains(lower, "fsmonitor") ||
		strings.HasPrefix(lower, "filter.") || strings.HasPrefix(lower, "alias.") || strings.Contains(lower, "include")
	if !executable {
		return Ruling{}, false
	}
	if found {
		command = strings.TrimSpace(command)
		if strings.HasPrefix(command, "!") {
			command = strings.TrimSpace(command[1:])
		}
		if command != "" {
			return Worst(Dangerous("git -c 注入可执行配置"), classifyCommandDepth(command, rules, depth+1)), true
		}
	}
	return Dangerous("git -c 注入可执行配置"), true
}

const curlValueShorts = "odFTDcKQXAbeHuUxmYyzw"

func classifyDownload(name string, args, ops []string) Ruling {
	for _, op := range ops {
		if op == "|" || op == "||" {
			return Confirm(KindShellPipe, "网络内容通过管道交给其他命令")
		}
	}
	getMode := false
	for _, arg := range args {
		if strings.EqualFold(arg, "--get") {
			getMode = true
			continue
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			letters := arg[1:]
			for len(letters) > 0 {
				if letters[0] == 'G' {
					getMode = true
					letters = letters[1:]
					continue
				}
				if strings.IndexByte(curlValueShorts, letters[0]) >= 0 {
					break
				}
				letters = letters[1:]
			}
		}
	}
	for i, arg := range args {
		lower := strings.ToLower(arg)
		if arg == "-K" || lower == "--config" || strings.HasPrefix(lower, "--config=") {
			return Confirm(KindWriteFS, "curl 配置文件可指定任意选项")
		}
		if lower == "--etag-save" || strings.HasPrefix(lower, "--etag-save=") {
			return Confirm(KindWriteFS, "curl 保存 ETag 文件")
		}
		if arg == "-J" || lower == "--remote-header-name" || lower == "--remote-name-all" {
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		}
		if lower == "--libcurl" || strings.HasPrefix(lower, "--libcurl=") {
			if (lower == "--libcurl" && i+1 < len(args) && args[i+1] == "-") || strings.HasPrefix(lower, "--libcurl=-") {
				continue
			}
			return Confirm(KindWriteFS, "curl 写入本地缓存或代码文件")
		}
		if lower == "--alt-svc" || strings.HasPrefix(lower, "--alt-svc=") || lower == "--hsts" || strings.HasPrefix(lower, "--hsts=") {
			return Confirm(KindWriteFS, "curl 写入本地缓存或代码文件")
		}
		if name == "wget" && (lower == "--output-document" || arg == "-O") {
			if i+1 < len(args) && args[i+1] == "-" {
				continue
			}
		}
		if name == "wget" && strings.HasPrefix(lower, "--output-document=-") {
			continue
		}
		if arg == "-Q" || lower == "--quote" || strings.HasPrefix(lower, "--quote=") {
			return Confirm(KindWriteFS, "curl 在服务器端执行命令")
		}
		if lower == "--stderr" {
			if i+1 < len(args) && args[i+1] != "-" {
				return Confirm(KindWriteFS, "curl 将错误输出写入文件")
			}
		}
		if strings.HasPrefix(lower, "--stderr=") && strings.TrimPrefix(lower, "--stderr=") != "-" {
			return Confirm(KindWriteFS, "curl 将错误输出写入文件")
		}
		if arg == "-D" || arg == "--dump-header" || arg == "--trace" || arg == "--trace-ascii" || strings.HasPrefix(arg, "-D") && len(arg) > 2 || strings.HasPrefix(lower, "--dump-header=") || strings.HasPrefix(lower, "--trace=") || strings.HasPrefix(lower, "--trace-ascii=") {
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		}
		switch arg {
		case "-o", "-O", "-F", "-T", "-c":
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		case "-d":
			if !getMode {
				return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
			}
		}
		switch lower {
		case "--remote-name", "--cookie-jar", "--output", "--output-document", "--form", "--form-string", "--upload-file", "--post-data", "--post-file", "--method", "--json":
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		case "--data", "--data-binary", "--data-raw", "--data-ascii", "--data-urlencode":
			if !getMode {
				return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
			}
		case "-X", "--request":
			if i+1 < len(args) && !strings.EqualFold(args[i+1], "GET") {
				return Confirm(KindWriteFS, "非 GET 网络请求")
			}
		}
		if strings.HasPrefix(lower, "--request=") && !strings.EqualFold(strings.TrimPrefix(lower, "--request="), "get") {
			return Confirm(KindWriteFS, "非 GET 网络请求")
		}
		for _, prefix := range []string{"--cookie-jar=", "--form=", "--form-string=", "--json=", "--output=", "--upload-file=", "--output-document=", "--post-data=", "--post-file=", "--method="} {
			if strings.HasPrefix(lower, prefix) {
				return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
			}
		}
		if !getMode {
			for _, prefix := range []string{"--data=", "--data-binary=", "--data-raw=", "--data-ascii=", "--data-urlencode="} {
				if strings.HasPrefix(lower, prefix) {
					return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
				}
			}
		}
		write, nonGet := curlShortOptionRisk(arg, getMode)
		if write {
			return Confirm(KindWriteFS, "下载保存或 HTTP 写入")
		}
		if nonGet {
			return Confirm(KindWriteFS, "非 GET 网络请求")
		}
	}
	if name == "wget" && !wgetWritesStdout(args) && !containsAny(args, "-qO-", "-O-", "--spider", "--server-response", "-S") {
		return Confirm(KindWriteFS, "wget 默认会保存文件")
	}
	return Allow()
}

func wgetWritesStdout(args []string) bool {
	for index, arg := range args {
		if (arg == "-O" || arg == "--output-document") && index+1 < len(args) && args[index+1] == "-" {
			return true
		}
		if strings.HasPrefix(arg, "--output-document=-") {
			return true
		}
	}
	return false
}

func curlShortOptionRisk(arg string, getMode bool) (bool, bool) {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false, false
	}
	for index := 1; index < len(arg); index++ {
		value := arg[index+1:]
		switch arg[index] {
		case 'o', 'O', 'F', 'T', 'D', 'c':
			return value != "-", false
		case 'd':
			return !getMode && value != "-", false
		case 'K', 'J':
			return true, false
		case 'Q':
			return true, false
		case 'X':
			return false, !strings.EqualFold(value, "GET")
		}
	}
	return false, false
}

func classifyRedis(args []string) Ruling {
	filtered := make([]string, 0, len(args))
	scan := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			switch arg {
			case "-h", "-p", "-a", "-s", "-d", "-D", "-r", "-i", "-X", "-t", "-k", "-n", "-u", "--user", "--pass", "--host", "--port", "--db", "--uri", "--socket", "--cert", "--key", "--cacert", "--cacertdir", "--capath", "--pattern", "--quoted-pattern", "--count", "--cursor", "--type", "--sni", "--lru-test", "--rdb", "--functions-rdb", "--pipe-timeout", "--memkeys-samples", "--keystats-samples", "--top", "--eval", "--cluster", "--show-pushes", "--name", "--tls-ciphers", "--tls-ciphersuites", "--intrinsic-latency", "--vset-recall", "--vset-recall-count", "-ef", "-ele":
				i++
			case "--scan":
				scan = true
			case "--no-raw", "--raw", "--csv", "--json", "--quoted-json", "--quoted-input", "--stat", "--bigkeys", "--hotkeys", "--memkeys", "--keystats", "--latency", "--latency-history", "--latency-dist", "--replica", "--pipe", "--trip", "--ldb", "--ldb-sync-mode", "--tls", "--insecure", "--verbose", "--no-auth-warning", "--askpass", "-2", "-3", "-4", "-6", "-c", "-e", "-v", "-x":
			default:
				return Confirm(KindUnknown, "redis-cli 包含无法识别的选项")
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	if scan && len(filtered) == 0 {
		return Allow()
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
