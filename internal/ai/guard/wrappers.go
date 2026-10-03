package guard

import (
	"strconv"
	"strings"
)

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
	forName      string
	pidMode      bool
	replace      string
	replaceSplit bool
	fileInput    string
	listOnly     bool
	removeOnly   bool
	nulMode      bool
	hasDelimiter bool
	delimiter    string
	maxArgs      string
	linesLimit   string
	sizeLimit    string
	noRunIfEmpty bool
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
	case "d", "delimiter":
		s.hasDelimiter = true
		s.delimiter = value
	case "n", "max-args":
		s.maxArgs = value
	case "L", "max-lines":
		s.linesLimit = value
	case "s", "max-chars":
		if s.forName == "xargs" {
			s.sizeLimit = value
		}
	}
}

func (s *wrapperScan) recordFlag(key string) {
	switch key {
	case "l":
		s.listOnly = true
	case "r", "d":
		if s.forName == "xargs" {
			s.noRunIfEmpty = true
		} else {
			s.removeOnly = true
		}
	case "i":
		// GNU -i is --replace with the default placeholder.
		if s.forName == "xargs" && s.replace == "" {
			s.replace = "{}"
		}
	case "0", "null":
		s.nulMode = true
	case "no-run-if-empty":
		s.noRunIfEmpty = true
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
	scan := wrapperScan{forName: name}
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

// classifyXargs models xargs semantics. With -I/-J/--replace, every
// occurrence of the replacement string in the initial arguments is
// substituted with one processed input line per invocation. Without it, the
// whole input stream is parsed into words (default quoting rules, or literal
// items under -0/-d) and the words are appended to the command: one single
// invocation by default, or batches of -n words. Anything that cannot be
// modeled exactly (-L, -s, unparseable input) fails closed to Danger.
func (c *classifier) classifyXargs(nested []string, scan wrapperScan, stdin pipeInput) Ruling {
	if scan.fileInput != "" {
		return Dangerous("xargs 从文件构建命令")
	}
	if stdin.kind == stdinFile {
		return Dangerous("xargs 从文件构建命令")
	}
	if stdin.kind != stdinPipe && stdin.kind != stdinHeredoc || !stdin.ok {
		if recursiveDeleteCommand(nested) {
			return Dangerous("xargs 以未受控输入调用递归删除")
		}
		return Dangerous("xargs 从标准输入构建命令")
	}
	if scan.replace != "" {
		return c.classifyXargsReplace(nested, scan, stdin.text)
	}
	if scan.linesLimit != "" || scan.sizeLimit != "" {
		return Dangerous("xargs 的分批参数无法精确建模")
	}
	if scan.nulMode && scan.hasDelimiter {
		return Dangerous("xargs 的分隔符参数冲突")
	}
	words, ok := xargsInputWords(scan, stdin.text)
	if !ok {
		return Dangerous("xargs 输入无法精确解析")
	}
	if len(words) == 0 {
		if scan.noRunIfEmpty {
			return Allow()
		}
		// With input but no words, xargs still runs the command once with
		// no appended arguments.
		return c.child().classifyArgv(nested, pipeInput{})
	}
	batch := len(words)
	if scan.maxArgs != "" {
		value, err := strconv.Atoi(scan.maxArgs)
		if err != nil || value <= 0 {
			return Indeterminate("xargs -n 参数无效")
		}
		batch = value
	}
	result := Allow()
	for start := 0; start < len(words); start += batch {
		end := start + batch
		if end > len(words) {
			end = len(words)
		}
		argv := make([]string, 0, len(nested)+end-start)
		argv = append(argv, nested...)
		argv = append(argv, words[start:end]...)
		result = Worst(result, c.child().classifyArgv(argv, pipeInput{}))
	}
	return result
}

// classifyXargsReplace handles -I/-J/--replace insert mode: one invocation
// per nonblank input line.
func (c *classifier) classifyXargsReplace(nested []string, scan wrapperScan, input string) Ruling {
	result := Allow()
	classified := 0
	for _, line := range strings.Split(input, "\n") {
		if line == "" {
			continue
		}
		classified++
		result = Worst(result, c.classifyXargsLine(nested, scan, line))
	}
	if classified == 0 {
		return Dangerous("xargs 从标准输入构建命令")
	}
	return result
}

// xargsInputWords parses the input stream into words for the non-replace
// modes: -0 and -d take items literally, the default mode applies xargs
// quoting rules.
func xargsInputWords(scan wrapperScan, input string) ([]string, bool) {
	switch {
	case scan.hasDelimiter:
		if scan.delimiter == "" {
			return nil, false
		}
		items := strings.Split(input, scan.delimiter[:1])
		if len(items) > 0 && items[len(items)-1] == "" {
			items = items[:len(items)-1]
		}
		return items, true
	case scan.nulMode:
		items := strings.Split(input, "\x00")
		if len(items) > 0 && items[len(items)-1] == "" {
			items = items[:len(items)-1]
		}
		return items, true
	default:
		return xargsWords(input)
	}
}

// xargsWords splits an xargs input stream into words with the default
// quoting rules: blanks separate words, single and double quotes group
// without becoming part of the word, and backslash escapes the next
// character (including a newline). It reports false when the input cannot be
// parsed exactly (unterminated quote or trailing backslash); real xargs then
// fails without running any command.
func xargsWords(input string) ([]string, bool) {
	var words []string
	var current strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
				continue
			}
			if quote == '"' && ch == '\\' && i+1 < len(input) {
				if next := input[i+1]; next == '"' || next == '\\' || next == '$' || next == '`' || next == '\n' {
					current.WriteByte(next)
					i++
					continue
				}
			}
			current.WriteByte(ch)
			continue
		}
		switch {
		case ch == '\'' || ch == '"':
			quote = ch
			inWord = true
		case ch == '\\':
			if i+1 >= len(input) {
				return nil, false
			}
			current.WriteByte(input[i+1])
			i++
			inWord = true
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\v' || ch == '\f':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteByte(ch)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if inWord {
		words = append(words, current.String())
	}
	return words, true
}

// xargsProcessedLine applies xargs quote and backslash processing to one
// input line while keeping blanks: -I replaces the placeholder with the
// whole processed line as one argument.
func xargsProcessedLine(line string) (string, bool) {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
				continue
			}
			if quote == '"' && ch == '\\' && i+1 < len(line) {
				if next := line[i+1]; next == '"' || next == '\\' || next == '$' || next == '`' || next == '\n' {
					out.WriteByte(next)
					i++
					continue
				}
			}
			out.WriteByte(ch)
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '\\':
			if i+1 >= len(line) {
				return "", false
			}
			out.WriteByte(line[i+1])
			i++
		default:
			out.WriteByte(ch)
		}
	}
	if quote != 0 {
		return "", false
	}
	return out.String(), true
}

// classifyXargsLine classifies one input line under replacement semantics.
// GNU -I replaces every placeholder occurrence with the whole processed line
// as one argument; BSD -J replaces only standalone placeholder tokens with
// the line's words. Because implementations disagree, both interpretations
// are classified and the worse ruling wins.
func (c *classifier) classifyXargsLine(nested []string, scan wrapperScan, line string) Ruling {
	processed, ok := xargsProcessedLine(line)
	if !ok {
		return Dangerous("xargs 输入无法精确解析")
	}
	substituted := make([]string, len(nested))
	for i, token := range nested {
		substituted[i] = strings.ReplaceAll(token, scan.replace, processed)
	}
	result := c.child().classifyArgv(substituted, pipeInput{})
	if scan.replaceSplit {
		words, ok := xargsWords(line)
		if !ok {
			return Worst(result, Dangerous("xargs 输入无法精确解析"))
		}
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
