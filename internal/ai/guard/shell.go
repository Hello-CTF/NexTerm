package guard

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

const (
	maxClassifyDepth = 8
	maxCommandBytes  = 64 << 10
	maxASTNodes      = 4096
)

type stdinKind uint8

const (
	stdinNone stdinKind = iota
	stdinPipe
	stdinHeredoc
	stdinFile
)

type pipeInput struct {
	kind stdinKind
	text string
	ok   bool
}

type classifier struct {
	rules []string
	depth int
	nodes *int
}

func ClassifyCommand(command string, rules []string) Ruling {
	nodes := 0
	return classifyCommandText(command, rules, 0, &nodes)
}

func classifyCommandText(command string, rules []string, depth int, nodes *int) (result Ruling) {
	result = Allow()
	if matchDangerRule(command, rules) {
		result = Dangerous("命中自定义危险规则")
	}
	if depth > maxClassifyDepth {
		return Worst(result, Indeterminate("命令嵌套过深，无法安全分析"))
	}
	if len(command) > maxCommandBytes {
		return Worst(result, Indeterminate("命令超出分析预算，无法安全分析"))
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return Worst(result, Indeterminate("命令无法可靠解析: "+err.Error()))
	}
	c := &classifier{rules: rules, depth: depth, nodes: nodes}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Worst(result, Indeterminate("命令结构异常，无法安全分析"))
		}
	}()
	return Worst(result, c.classifyStmts(file.Stmts, pipeInput{}))
}

func (c *classifier) classifyText(text string) Ruling {
	return classifyCommandText(text, c.rules, c.depth+1, c.nodes)
}

func (c *classifier) child() *classifier {
	return &classifier{rules: c.rules, depth: c.depth + 1, nodes: c.nodes}
}

func (c *classifier) overBudget() bool {
	*c.nodes++
	return *c.nodes > maxASTNodes
}

func (c *classifier) classifyStmts(stmts []*syntax.Stmt, pipeIn pipeInput) Ruling {
	result := Allow()
	for _, stmt := range stmts {
		result = Worst(result, c.classifyStmt(stmt, pipeIn))
		if result.Risk == Forbidden {
			return result
		}
	}
	return result
}

func (c *classifier) classifyStmt(stmt *syntax.Stmt, pipeIn pipeInput) Ruling {
	if c.overBudget() {
		return Indeterminate("命令结构超出分析预算")
	}
	if stmt == nil {
		return Allow()
	}

	redirResult, stdin := c.classifyRedirects(stmt.Redirs)
	if stdin != nil {
		pipeIn = *stdin
	}
	result := redirResult
	switch cmd := stmt.Cmd.(type) {
	case nil:

	case *syntax.CallExpr:
		result = Worst(result, c.classifyCallExpr(cmd, pipeIn))
	case *syntax.BinaryCmd:
		result = Worst(result, c.classifyBinaryCmd(cmd, pipeIn))
	case *syntax.Subshell:
		result = Worst(result, c.classifyStmts(cmd.Stmts, pipeIn))
	case *syntax.Block:
		result = Worst(result, c.classifyStmts(cmd.Stmts, pipeIn))
	case *syntax.IfClause:
		result = Worst(result, c.classifyIfClause(cmd, pipeIn))
	case *syntax.WhileClause:
		result = Worst(result, c.classifyStmts(cmd.Cond, pipeIn), c.classifyStmts(cmd.Do, pipeIn))
	case *syntax.ForClause:
		result = Worst(result, c.classifyForClause(cmd, pipeIn))
	case *syntax.CaseClause:
		result = Worst(result, c.classifyCaseClause(cmd, pipeIn))
	case *syntax.FuncDecl:

		result = Worst(result, Indeterminate("函数定义会使后续调用无法审查"))
	case *syntax.ArithmCmd:

		ruling := Allow()
		c.classifyArithmExpr(cmd.X, &ruling)
		result = Worst(result, ruling, Indeterminate("算术命令无法证明有界"))
	case *syntax.LetClause:
		ruling := Allow()
		for _, expr := range cmd.Exprs {
			c.classifyArithmExpr(expr, &ruling)
		}
		result = Worst(result, ruling, Indeterminate("算术命令无法证明有界"))
	case *syntax.TestClause:

		result = Worst(result, c.classifyTestExpr(cmd.X))
	case *syntax.DeclClause:
		for _, assign := range cmd.Args {
			result = Worst(result, c.classifyAssign(assign))
		}
	case *syntax.TimeClause:
		result = Worst(result, c.classifyStmt(cmd.Stmt, pipeIn))
	case *syntax.CoprocClause:

		result = Worst(result, c.classifyStmt(cmd.Stmt, pipeInput{kind: stdinPipe}))
	default:
		result = Worst(result, Indeterminate("命令结构无法识别"))
	}
	return result
}

func (c *classifier) classifyTestExpr(expr syntax.TestExpr) Ruling {
	result := Allow()
	if expr == nil {
		return result
	}
	switch e := expr.(type) {
	case *syntax.BinaryTest:
		result = Worst(result, c.classifyTestExpr(e.X), c.classifyTestExpr(e.Y))
	case *syntax.UnaryTest:
		result = Worst(result, c.classifyTestExpr(e.X))
	case *syntax.ParenTest:
		result = Worst(result, c.classifyTestExpr(e.X))
	case *syntax.Word:
		if _, ok := c.literalWord(e, &result); !ok {
			result = Worst(result, Indeterminate("测试表达式包含动态替换"))
		}
	}
	return result
}

func (c *classifier) classifyIfClause(clause *syntax.IfClause, pipeIn pipeInput) Ruling {
	if clause == nil {
		return Allow()
	}
	return Worst(c.classifyStmts(clause.Cond, pipeIn), c.classifyStmts(clause.Then, pipeIn), c.classifyIfClause(clause.Else, pipeIn))
}

func (c *classifier) classifyForClause(clause *syntax.ForClause, pipeIn pipeInput) Ruling {
	result := c.classifyStmts(clause.Do, pipeIn)
	switch loop := clause.Loop.(type) {
	case *syntax.WordIter:
		for _, item := range loop.Items {
			ruling := Allow()
			if _, ok := c.literalWord(item, &ruling); !ok {
				result = Worst(result, Indeterminate("循环迭代值包含动态替换"), ruling)
			}
		}
	case *syntax.CStyleLoop:

		ruling := Allow()
		c.classifyArithmExpr(loop.Init, &ruling)
		c.classifyArithmExpr(loop.Cond, &ruling)
		c.classifyArithmExpr(loop.Post, &ruling)
		result = Worst(result, ruling, Indeterminate("C 风格循环无法证明有界"))
	default:
		result = Worst(result, Indeterminate("C 风格循环无法证明有界"))
	}
	return result
}

func (c *classifier) classifyCaseClause(clause *syntax.CaseClause, pipeIn pipeInput) Ruling {
	result := Allow()
	ruling := Allow()
	if _, ok := c.literalWord(clause.Word, &ruling); !ok {
		result = Worst(result, Indeterminate("case 取值包含动态替换"), ruling)
	}
	for _, item := range clause.Items {

		for _, pattern := range item.Patterns {
			ruling := Allow()
			if _, ok := c.literalWord(pattern, &ruling); !ok {
				result = Worst(result, Indeterminate("case 模式包含动态替换"), ruling)
			}
		}
		for _, body := range item.Stmts {
			result = Worst(result, c.classifyStmt(body, pipeIn))
		}
	}
	return result
}

func (c *classifier) classifyBinaryCmd(cmd *syntax.BinaryCmd, pipeIn pipeInput) Ruling {
	if c.overBudget() {
		return Indeterminate("命令结构超出分析预算")
	}
	switch cmd.Op {
	case syntax.AndStmt, syntax.OrStmt:
		return Worst(c.classifyStmt(cmd.X, pipeIn), c.classifyStmt(cmd.Y, pipeIn))
	case syntax.Pipe, syntax.PipeAll:
		left := c.classifyStmt(cmd.X, pipeIn)
		text, ok := c.pipeProducer(cmd.X)
		right := c.classifyStmt(cmd.Y, pipeInput{kind: stdinPipe, text: text, ok: ok})
		return Worst(left, right)
	}
	return Indeterminate("无法识别的命令连接符")
}

func (c *classifier) pipeProducer(stmt *syntax.Stmt) (string, bool) {
	if stmt == nil {
		return "", false
	}
	if bin, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		if bin.Op == syntax.Pipe || bin.Op == syntax.PipeAll {
			return c.pipeProducer(bin.Y)
		}
		return "", false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "", false
	}
	discarded := Allow()
	argv := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		text, ok := c.literalWord(word, &discarded)
		if !ok {
			return "", false
		}
		argv = append(argv, text)
	}
	switch commandName(argv[0]) {
	case "echo":
		args := argv[1:]
		for len(args) > 0 && echoOption(args[0]) {
			args = args[1:]
		}
		for _, arg := range args {
			if strings.ContainsRune(arg, '\\') {
				return "", false
			}
		}
		return strings.Join(args, " "), true
	case "printf":
		return printfProducer(argv[1:])
	case "cat":

		for _, arg := range argv[1:] {
			if arg == "-" || strings.HasPrefix(arg, "-") {
				continue
			}
			return "", false
		}

		var hdoc *syntax.Redirect
		for _, r := range stmt.Redirs {
			switch r.Op {
			case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc, syntax.RdrIn, syntax.RdrInOut, syntax.DplIn:
				hdoc = r
			}
		}
		if hdoc == nil {
			return "", false
		}
		switch hdoc.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			if hdoc.Hdoc == nil {
				return "", false
			}
			body, ok := c.literalWord(hdoc.Hdoc, &discarded)
			return body, ok
		case syntax.WordHdoc:
			text, ok := c.literalWord(hdoc.Word, &discarded)
			return text + "\n", ok
		}
		return "", false
	}
	return "", false
}

func printfProducer(args []string) (string, bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		if args[0] == "--" {

			args = args[1:]
			break
		}
		if args[0] == "-v" {

			return "", true
		}

		return "", true
	}
	if len(args) == 0 {
		return "", true
	}
	format := args[0]
	rest := args[1:]
	argIndex := 0
	var out strings.Builder
	for {
		consumed := 0
		takeArg := func(numeric bool) string {
			if argIndex < len(rest) {
				value := rest[argIndex]
				argIndex++
				consumed++
				return value
			}
			if numeric {
				return "0"
			}
			return ""
		}
		for i := 0; i < len(format); i++ {
			ch := format[i]
			if ch == '\\' {
				if i+1 >= len(format) {
					return "", false
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
					return out.String(), true
				default:
					return "", false
				}
				continue
			}
			if ch != '%' {
				out.WriteByte(ch)
				continue
			}
			if i+1 >= len(format) {
				return "", false
			}
			i++
			for i < len(format) && strings.ContainsRune("-+ #0.123456789", rune(format[i])) {
				i++
			}
			if i >= len(format) {
				return "", false
			}
			switch format[i] {
			case '%':
				out.WriteByte('%')
			case 's', 'c':
				out.WriteString(takeArg(false))
			case 'd', 'i', 'o', 'u', 'x', 'X', 'f', 'e', 'E', 'g', 'G':
				out.WriteString(takeArg(true))
			default:
				return "", false
			}
		}

		if consumed == 0 || argIndex >= len(rest) {
			break
		}
	}
	return out.String(), true
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

func (c *classifier) classifyRedirects(redirs []*syntax.Redirect) (Ruling, *pipeInput) {
	result := Allow()
	var stdin *pipeInput
	writes := 0
	lastWriteTarget := ""
	for _, r := range redirs {
		ruling, write, target, in := c.classifyRedirect(r)
		result = Worst(result, ruling)
		if result.Risk == Forbidden {
			return result, nil
		}
		if write {
			writes++
			lastWriteTarget = target
		}
		if in != nil {
			stdin = in
		}
	}

	if writes > 0 && !(writes == 1 && lastWriteTarget == "/dev/null") {
		result = Worst(result, Confirm(KindWriteFS, "包含文件重定向"))
	}
	return result, stdin
}

func (c *classifier) classifyRedirect(r *syntax.Redirect) (Ruling, bool, string, *pipeInput) {
	switch r.Op {
	case syntax.Hdoc, syntax.DashHdoc:
		return c.classifyHeredoc(r)
	case syntax.WordHdoc:
		result := Allow()
		text, ok := c.literalWord(r.Word, &result)
		if !ok {
			result = Worst(result, Indeterminate("here-string 包含动态替换"))
		}
		return result, false, "", &pipeInput{kind: stdinHeredoc, text: text + "\n", ok: ok}
	case syntax.RdrIn:
		target, ruling := c.redirectTarget(r)
		if isNetworkDevice(target) {
			return Worst(ruling, Dangerous("重定向到网络设备")), false, "", nil
		}
		return ruling, false, "", &pipeInput{kind: stdinFile}
	case syntax.RdrInOut:
		target, ruling := c.redirectTarget(r)
		if isNetworkDevice(target) {
			return Worst(ruling, Dangerous("重定向到网络设备")), false, "", nil
		}
		return Worst(ruling, writeTargetRuling(target)), true, target, nil
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
		target, ruling := c.redirectTarget(r)
		if isNetworkDevice(target) {
			return Worst(ruling, Dangerous("重定向到网络设备")), false, "", nil
		}
		return Worst(ruling, writeTargetRuling(target)), true, target, nil
	case syntax.DplOut:
		result := Allow()
		word, ok := c.literalWord(r.Word, &result)
		if !ok {
			return Worst(result, Indeterminate("重定向目标包含动态替换")), false, "", nil
		}
		if word == "-" || isNumeric(word) {
			return result, false, "", nil
		}

		if isNetworkDevice(word) {
			return Worst(result, Dangerous("重定向到网络设备")), false, "", nil
		}
		return Worst(result, writeTargetRuling(word)), true, word, nil
	case syntax.DplIn:
		result := Allow()
		word, ok := c.literalWord(r.Word, &result)
		if !ok {
			return Worst(result, Indeterminate("重定向目标包含动态替换")), false, "", nil
		}
		if word == "-" || isNumeric(word) {
			return result, false, "", nil
		}
		return result, false, "", &pipeInput{kind: stdinFile}
	}
	return Indeterminate("无法识别的重定向"), false, "", nil
}

func (c *classifier) classifyHeredoc(r *syntax.Redirect) (Ruling, bool, string, *pipeInput) {
	if r.N != nil && r.N.Value != "" && r.N.Value != "0" {
		return Indeterminate("非常规文件描述符重定向无法证明有界"), false, "", nil
	}
	if r.Hdoc == nil {
		return Indeterminate("here-document 内容缺失"), false, "", nil
	}
	result := Allow()
	text, ok := c.literalWord(r.Hdoc, &result)
	if !ok {
		result = Worst(result, Indeterminate("here-document 包含动态替换"))
	}
	return result, false, "", &pipeInput{kind: stdinHeredoc, text: text, ok: ok}
}

func (c *classifier) redirectTarget(r *syntax.Redirect) (string, Ruling) {
	if r.N != nil && r.N.Value != "" && !isNumeric(r.N.Value) {
		return "", Indeterminate("非常规文件描述符重定向无法证明有界")
	}
	result := Allow()
	target, ok := c.literalWord(r.Word, &result)
	if !ok {
		return "", Worst(result, Indeterminate("重定向目标包含动态替换"))
	}
	if strings.ContainsAny(target, "*?[") {
		return "", Worst(result, Indeterminate("重定向目标包含通配符"))
	}
	return target, result
}

func writeTargetRuling(target string) Ruling {
	if isCriticalWriteTarget(target) {
		return Deny("禁止写入关键系统路径")
	}
	if isBlockDevice(target) {
		return Deny("禁止直接写入块设备")
	}
	return Allow()
}

func isNetworkDevice(target string) bool {
	return strings.Contains(target, "/dev/tcp/") || strings.Contains(target, "/dev/udp/")
}

func isNumeric(value string) bool {
	_, err := strconv.Atoi(value)
	return err == nil
}

func (c *classifier) literalArgv(words []*syntax.Word) ([]string, bool, Ruling) {
	result := Allow()
	argv := make([]string, 0, len(words))
	provable := true
	for _, word := range words {
		text, ok := c.literalWord(word, &result)
		if !ok {
			provable = false
		}
		argv = append(argv, text)
	}
	if !provable {
		result = Worst(Indeterminate("命令包含动态替换，无法证明其内容"), result)
	}
	return argv, provable, result
}

func (c *classifier) literalWord(word *syntax.Word, result *Ruling) (string, bool) {
	if word == nil {
		return "", true
	}
	return c.literalParts(word.Parts, result)
}

func (c *classifier) literalParts(parts []syntax.WordPart, result *Ruling) (string, bool) {
	var out strings.Builder
	ok := true
	for _, part := range parts {
		text, partOK := c.literalPart(part, result)
		out.WriteString(text)
		ok = ok && partOK
	}
	return out.String(), ok
}

func (c *classifier) literalPart(part syntax.WordPart, result *Ruling) (string, bool) {
	switch p := part.(type) {
	case *syntax.Lit:
		return p.Value, true
	case *syntax.SglQuoted:
		if p.Dollar {

			return "", false
		}
		return p.Value, true
	case *syntax.DblQuoted:
		if p.Dollar {
			return "", false
		}
		return c.literalParts(p.Parts, result)
	case *syntax.CmdSubst:
		*result = Worst(*result, c.classifySubstStmts(p.Stmts))
		return "", false
	case *syntax.ProcSubst:
		*result = Worst(*result, c.classifySubstStmts(p.Stmts))
		return "", false
	case *syntax.ParamExp:
		c.classifyParamExp(p, result)
		return "", false
	case *syntax.ArithmExp:
		c.classifyArithmExpr(p.X, result)
		return "", false
	default:

		return "", false
	}
}

func (c *classifier) classifyParamExp(exp *syntax.ParamExp, result *Ruling) {
	if exp == nil {
		return
	}
	c.classifyArithmExpr(exp.Index, result)
	if exp.Slice != nil {
		c.classifyArithmExpr(exp.Slice.Offset, result)
		c.classifyArithmExpr(exp.Slice.Length, result)
	}
	if exp.Repl != nil {
		c.literalWord(exp.Repl.Orig, result)
		c.literalWord(exp.Repl.With, result)
	}
	if exp.Exp != nil {
		c.literalWord(exp.Exp.Word, result)
	}
}

func (c *classifier) classifyArithmExpr(expr syntax.ArithmExpr, result *Ruling) {
	switch e := expr.(type) {
	case *syntax.BinaryArithm:
		c.classifyArithmExpr(e.X, result)
		c.classifyArithmExpr(e.Y, result)
	case *syntax.UnaryArithm:
		c.classifyArithmExpr(e.X, result)
	case *syntax.ParenArithm:
		c.classifyArithmExpr(e.X, result)
	case *syntax.Word:
		c.literalWord(e, result)
	}
}

func (c *classifier) classifySubstStmts(stmts []*syntax.Stmt) Ruling {
	child := c.child()
	result := Allow()
	for _, stmt := range stmts {
		result = Worst(result, child.classifyStmt(stmt, pipeInput{}))
	}
	return result
}

var dangerousEnvAssignments = map[string]bool{
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "LD_AUDIT": true,
	"DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
	"BASH_ENV": true, "ENV": true,
	"NODE_OPTIONS": true, "RUBYOPT": true, "PERL5OPT": true, "PYTHONSTARTUP": true,
}

func dangerousAssignmentName(value string) bool {
	name, _, found := strings.Cut(value, "=")
	return found && dangerousEnvAssignments[name]
}

func (c *classifier) classifyAssign(assign *syntax.Assign) Ruling {
	if assign == nil {
		return Allow()
	}
	if assign.Array != nil || assign.Index != nil {

		ruling := Allow()
		c.classifyArithmExpr(assign.Index, &ruling)
		if assign.Array != nil {
			for _, elem := range assign.Array.Elems {
				c.classifyArithmExpr(elem.Index, &ruling)
				c.literalWord(elem.Value, &ruling)
			}
		}
		return Worst(ruling, Indeterminate("数组或下标赋值无法证明有界"))
	}
	result := Allow()
	if assign.Value != nil {
		if _, ok := c.literalWord(assign.Value, &result); !ok {
			result = Worst(Indeterminate("赋值包含动态替换，无法证明有界"), result)
		}
	}
	if assign.Name != nil && dangerousEnvAssignments[assign.Name.Value] {
		result = Worst(result, Dangerous("赋值注入可执行环境变量"))
	}
	return result
}

func (c *classifier) classifyCallExpr(cmd *syntax.CallExpr, stdin pipeInput) Ruling {
	result := Allow()
	for _, assign := range cmd.Assigns {
		result = Worst(result, c.classifyAssign(assign))
	}
	if len(cmd.Args) == 0 {
		return result
	}
	argv, provable, ruling := c.literalArgv(cmd.Args)
	result = Worst(result, ruling)
	if !provable || result.Risk == Forbidden {
		return result
	}

	if argv[0] != "[" && strings.ContainsAny(argv[0], "*?[") {
		return Worst(result, Indeterminate("命令名包含通配符"))
	}
	return Worst(result, c.classifyArgv(argv, stdin))
}

func (c *classifier) classifyArgv(argv []string, stdin pipeInput) Ruling {
	if c.depth > maxClassifyDepth {
		return Indeterminate("命令嵌套过深，无法安全分析")
	}
	if len(argv) == 0 {
		return Allow()
	}
	name := commandName(argv[0])
	args := argv[1:]
	switch name {
	case "sudo", "doas", "sudoedit":
		return c.classifySudo(name, args, stdin)
	case "su":
		return c.classifySu(args, stdin)
	case "env":
		return c.classifyEnv(args, stdin)
	}
	if wrapper, ok := execWrappers[name]; ok {
		return c.classifyWrapper(name, wrapper, args, stdin)
	}
	return c.classifySimple(argv, stdin)
}

const sudoFlagShorts = "ABbeEHiKklnPsvV"

func (c *classifier) classifySudo(name string, args []string, stdin pipeInput) Ruling {
	edit := name == "sudoedit"
	nonExec := false
	rest := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			rest = args[index+1:]
			break
		}
		if strings.HasPrefix(arg, "--") {
			option, _, attached, ok := resolveGNULongOption(arg, sudoLongOptions)
			if !ok {
				return Indeterminate(name + " 包含无法识别的长选项")
			}
			if option.name == "edit" {
				edit = true
			}
			switch option.name {
			case "list", "validate", "version", "help", "reset-timestamp", "kill":
				nonExec = true
			}
			if option.takesValue && !attached {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			letters := arg[1:]
			for len(letters) > 0 {
				letter := letters[0]
				switch {
				case letter == 'e':
					edit = true
					letters = letters[1:]
				case strings.IndexByte(sudoValueShorts, letter) >= 0:
					if len(letters) == 1 {
						index++
					}
					letters = ""
				case strings.IndexByte(sudoFlagShorts, letter) >= 0:
					if strings.IndexByte("lvVkK", letter) >= 0 {
						nonExec = true
					}
					letters = letters[1:]
				default:
					return Indeterminate(name + " 包含无法识别的选项")
				}
			}
			continue
		}
		rest = args[index:]
		break
	}
	result := Confirm(KindSudo, "包含权限提升")
	if edit {
		for _, target := range rest {
			if isCriticalWriteTarget(target) {
				return Deny("禁止以 root 编辑关键系统路径")
			}
		}
		return Worst(result, Dangerous("以 root 编辑文件"))
	}
	if len(rest) == 0 {
		if nonExec {
			return result
		}
		return Worst(result, Dangerous(name+" 启动特权会话"))
	}
	return Worst(result, c.child().classifyArgv(rest, stdin))
}

func (c *classifier) classifySu(args []string, stdin pipeInput) Ruling {
	result := Confirm(KindSudo, "包含身份切换")
	code := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if arg == "-" {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			option, value, attached, ok := resolveGNULongOption(arg, suLongOptions)
			if !ok {
				return Indeterminate("su 包含无法识别的长选项")
			}
			switch option.name {
			case "command", "session-command":
				if !attached && index+1 < len(args) {
					index++
					value = args[index]
				}
				code = value
			case "shell", "group", "supp-group", "whitelist-environment":
				if !attached {
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
					value := letters[1:]
					if value == "" && index+1 < len(args) {
						index++
						value = args[index]
					}
					code = value
					letters = ""
				case letter == 'l' || letter == 'm' || letter == 'p':
					letters = letters[1:]
				case letter == 's' || letter == 'g' || letter == 'G' || letter == 'w':
					if len(letters) == 1 {
						index++
					}
					letters = ""
				default:
					return Indeterminate("su 包含无法识别的选项")
				}
			}
			continue
		}

	}
	if code != "" {
		return Worst(result, c.classifyText(code))
	}
	if stdin.kind == stdinPipe || stdin.kind == stdinHeredoc {
		if !stdin.ok {
			return Worst(result, Dangerous("管道输入无法完整确定，执行器可能运行未审查的内容"))
		}
		return Worst(result, Confirm(KindSudo, "su 从标准输入执行命令"), c.classifyText(stdin.text))
	}
	return Worst(result, Dangerous("su 启动身份切换会话"))
}

func (c *classifier) classifyEnv(args []string, stdin pipeInput) Ruling {
	index := 0
	for index < len(args) {
		arg := args[index]
		if arg == "--" || arg == "-" {
			index++
			break
		}
		if strings.HasPrefix(arg, "--") {
			option, value, attached, ok := resolveGNULongOption(arg, envLongOptions)
			if !ok {
				return Indeterminate("env 包含无法识别的长选项")
			}
			if option.name == "split-string" {
				if !attached {
					index++
					if index >= len(args) {
						return Indeterminate("env 字符串拆句缺少内容")
					}
					value = args[index]
				}
				return c.classifyEnvSplitString(value, args[index+1:])
			}
			if option.takesValue && !attached {
				index++
			}
			index++
			continue
		}
		if strings.HasPrefix(arg, "-") {
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
					payload := letters[1:]
					if payload == "" {
						index++
						if index >= len(args) {
							return Indeterminate("env 字符串拆句缺少内容")
						}
						payload = args[index]
					}
					return c.classifyEnvSplitString(payload, args[index+1:])
				default:
					return Indeterminate("env 包含无法识别的选项")
				}
			}
			index++
			continue
		}
		if isAssignment(arg) {
			if dangerousAssignmentName(arg) {
				return Dangerous("赋值注入可执行环境变量")
			}
			index++
			continue
		}
		break
	}
	rest := args[index:]
	if len(rest) == 0 {

		return Allow()
	}
	return c.child().classifyArgv(rest, stdin)
}

func (c *classifier) classifyEnvSplitString(payload string, rest []string) Ruling {
	payload = strings.ReplaceAll(payload, "\\_", " ")
	for _, extra := range rest {
		payload += " " + quoteShellToken(extra)
	}
	return Worst(Confirm(KindUnknown, "env 的字符串拆句执行需要确认"), c.classifyText(payload))
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

const interpreterCodeLetters = "ceErp"

func (c *classifier) classifySimple(argv []string, stdin pipeInput) Ruling {
	name := commandName(argv[0])
	args := argv[1:]
	if isInterpreter(name) {
		return c.classifyInterpreter(name, args, stdin)
	}
	if ruling, ok := forbiddenCommand(name, args); ok {
		return ruling
	}
	if ruling, ok := dangerousCommand(name, args); ok {
		return ruling
	}
	if matchDangerRule(strings.Join(argv, " "), c.rules) {
		return Dangerous("命中自定义危险规则")
	}
	if ruling, ok := classifyStateChangingBuiltins(name, args); ok {
		return ruling
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
		return c.classifyDocker(args, stdin)
	case "kubectl":
		return c.classifyKubectl(args, stdin)
	case "git":
		return c.classifyGit(args)
	case "redis-cli", "valkey-cli":
		result := classifyRedis(args)
		if stdin.kind == stdinPipe || stdin.kind == stdinHeredoc {

			if !stdin.ok {
				return Worst(result, Dangerous("管道输入无法完整确定，执行器可能运行未审查的内容"))
			}
			result = Worst(result, Confirm(KindUnknown, "Redis 客户端从标准输入执行命令"))
			for _, line := range strings.Split(stdin.text, "\n") {
				if fields := strings.Fields(line); len(fields) > 0 {
					result = Worst(result, classifyRedis(fields))
				}
			}
		}
		return result
	case "mysql", "mariadb", "psql":
		return c.classifyDatabaseClient(name, args, stdin)
	case "sed", "awk", "gawk", "ed", "vim", "vi", "nano", "emacs":
		return Confirm(KindUnknown, "命令具有编辑或执行子命令能力")
	case "find":
		return c.classifyFind(args)
	case "tar":
		return c.classifyTar(args)
	case "gzip", "gunzip", "bzip2", "xz", "zip":
		return Confirm(KindWriteFS, "压缩工具可能修改文件")
	case "unzip":
		if containsAny(args, "-l", "-Z", "--list") {
			return Allow()
		}
		return Confirm(KindWriteFS, "解压会写入文件")
	case "curl", "wget":
		return classifyDownload(name, args)
	case "nc", "ncat", "netcat", "socat":
		for _, arg := range args {
			lower := strings.ToLower(arg)
			if lower == "--exec" || strings.HasPrefix(lower, "--exec=") || lower == "--sh-exec" || strings.HasPrefix(lower, "--sh-exec=") || lower == "--lua-exec" || strings.HasPrefix(lower, "--lua-exec=") {
				return Dangerous("网络工具执行命令")
			}
			if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg[1:], "ec") {

				return Dangerous("网络工具执行命令")
			}
			if strings.HasPrefix(lower, "exec:") || strings.HasPrefix(lower, "system:") || strings.HasPrefix(lower, "shell") {
				return Dangerous("网络工具执行命令")
			}
		}
		return Confirm(KindUnknown, "网络工具需要确认")
	case "ssh":
		return c.classifySSH(args)
	case "scp", "sftp", "rsync":
		return c.classifyRemoteTransfer(name, args)
	case "make", "gmake", "nmake", "bmake":
		return classifyMake(args)
	case "ninja":
		return classifyNinja(args)
	case "cmake":
		if containsAny(args, "--build", "--install") {
			return Dangerous("cmake 执行构建或安装脚本，内容无法验证")
		}
		return Confirm(KindWriteFS, "cmake 生成构建文件")
	case "alias":
		if len(args) == 0 {
			return Allow()
		}

		return Dangerous("别名定义会使后续命令无法审查")
	case "unalias":
		return Dangerous("别名删除会改变命令解析")
	}
	if safeFirstToken[name] {
		return Allow()
	}
	if stdin.kind == stdinPipe || stdin.kind == stdinHeredoc {
		return Indeterminate("管道输入进入无法识别的命令，无法证明有界")
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

func (c *classifier) classifyInterpreter(name string, args []string, stdin pipeInput) Ruling {
	shell := isShellInterpreter(name)
	var codes []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if shell {
			switch {
			case arg == "-c":
				if i+1 < len(args) {
					codes = append(codes, args[i+1])
				}
			case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.IndexByte(arg[1:], 'c') >= 0:

				position := strings.IndexByte(arg[1:], 'c')
				if remainder := arg[position+2:]; remainder != "" {
					codes = append(codes, remainder)
				}
				if i+1 < len(args) {
					codes = append(codes, args[i+1])
				}
			}
			continue
		}
		lower := strings.ToLower(arg)
		switch {
		case arg == "-c" || arg == "-e" || arg == "-E" || arg == "-r" || arg == "-p" || strings.EqualFold(arg, "-Command") || arg == "/c" || arg == "--eval" || arg == "--command" || arg == "--execute":
			if i+1 < len(args) {
				codes = append(codes, args[i+1])
			}
		case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--"):
			letters := arg[1:]
			for len(letters) > 0 {
				if strings.IndexByte(interpreterCodeLetters, letters[0]) >= 0 {
					codes = append(codes, letters[1:])
					letters = ""
					continue
				}
				letters = letters[1:]
			}
		case strings.HasPrefix(lower, "--eval=") || strings.HasPrefix(lower, "--command=") || strings.HasPrefix(lower, "--execute="):
			codes = append(codes, arg[strings.Index(arg, "=")+1:])
		}
	}
	if len(codes) > 0 {
		if shell {
			result := Confirm(KindUnknown, "解释器或脚本执行需要确认")
			for _, code := range codes {
				result = Worst(result, c.classifyText(code))
			}
			return result
		}
		return Worst(Dangerous("解释器内联执行任意代码"), dangerousInterpreterText(strings.Join(codes, " "), c.rules))
	}
	if stdin.kind != stdinNone {
		if stdin.kind == stdinFile {
			return Dangerous(name + " 执行文件中的代码，内容无法验证")
		}
		if !stdin.ok {
			return Dangerous("管道输入无法完整确定，执行器可能运行未审查的内容")
		}
		if shell {
			return Worst(Confirm(KindUnknown, "解释器或脚本执行需要确认"), c.classifyText(stdin.text))
		}
		return Dangerous(name + " 从标准输入执行代码")
	}
	for _, arg := range args {
		if arg == "-" {
			return Dangerous(name + " 从标准输入执行代码")
		}
		if !strings.HasPrefix(arg, "-") {
			return Confirm(KindUnknown, "解释器或脚本执行需要确认")
		}
	}
	return Dangerous("解释器会话可执行任意代码")
}

func dangerousInterpreterText(value string, rules []string) Ruling {
	if matchDangerRule(value, rules) || containsDestructiveText(value) {
		return Dangerous("解释器代码包含危险操作")
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

func (c *classifier) classifyDatabaseClient(name string, args []string, stdin pipeInput) Ruling {
	result := Confirm(KindDBWrite, "交互式数据库客户端需要确认")
	if stdin.kind == stdinFile {
		return Dangerous("数据库客户端执行重定向 SQL 文件")
	}
	if stdin.kind == stdinPipe || stdin.kind == stdinHeredoc {

		if !stdin.ok {
			return Dangerous("管道输入无法完整确定，执行器可能运行未审查的内容")
		}
		result = Worst(result, ClassifySQL(stdin.text, c.rules))
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
			result = Worst(result, classifyDatabaseMetaCommand(name, payload, c))
			result = Worst(result, ClassifySQL(payload, c.rules))
		}
	}
	return result
}

func classifyDatabaseMetaCommand(name, payload string, c *classifier) Ruling {
	if name == "psql" {
		trimmed := strings.TrimSpace(payload)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(trimmed, `\!`):
			return Worst(Dangerous("psql 执行 shell 转义命令"), c.classifyText(strings.TrimSpace(trimmed[2:])))
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
			return Worst(Dangerous("mysql 执行 shell 转义命令"), c.classifyText(strings.TrimSpace(trimmed[len("system"):])))
		}
	}
	return Allow()
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

func (c *classifier) classifyFind(args []string) Ruling {
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
				result = Worst(result, Dangerous("find 执行外部命令"), c.classifyText(strings.Join(args[index+1:end], " ")))
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

const sshValueShorts = "BbcDeEFIiJLlmOoPpQRSWw"
const sshFlagShorts = "46ACfGgKkMNnqstTVvXxYy"

func (c *classifier) classifySSH(args []string) Ruling {
	result := Confirm(KindUnknown, "远程传输或执行需要确认")
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if strings.HasPrefix(arg, "--") {

			return Worst(result, Indeterminate("ssh 包含无法识别的长选项"))
		}
		index++
		letters := arg[1:]
		for len(letters) > 0 {
			letter := letters[0]
			switch {
			case letter == 'o':
				value := letters[1:]
				if value == "" {
					if index < len(args) {
						value = args[index]
						index++
					}
				}
				if value != "" {
					result = Worst(result, c.classifySSHConfigValue(value))
				}
				letters = ""
			case strings.IndexByte(sshValueShorts, letter) >= 0:
				if len(letters) == 1 && index < len(args) {
					index++
				}
				letters = ""
			case strings.IndexByte(sshFlagShorts, letter) >= 0:
				letters = letters[1:]
			default:
				return Worst(result, Indeterminate("ssh 包含无法识别的选项"))
			}
		}
	}
	if index >= len(args) {
		return result
	}
	index++
	if index >= len(args) {
		return result
	}

	return Worst(result, c.classifyText(strings.Join(args[index:], " ")))
}

func (c *classifier) classifySSHConfigValue(value string) Ruling {
	value = strings.TrimSpace(value)
	separator := strings.IndexAny(value, "= \t")
	if separator < 0 {
		return Allow()
	}
	key := strings.TrimSpace(value[:separator])
	command := strings.TrimSpace(value[separator+1:])
	command = strings.TrimSpace(strings.TrimPrefix(command, "="))
	switch strings.ToLower(key) {
	case "proxycommand", "localcommand", "remotecommand", "knownhostscommand", "xauthlocation":
		if command == "" || strings.EqualFold(command, "none") {
			return Allow()
		}
		return Worst(Dangerous("ssh 选项执行本地或远端命令"), c.classifyText(command))
	case "include":

		return Dangerous("ssh Include 引入配置文件，内容无法验证")
	}
	return Allow()
}

const makeValueShorts = "CjloW"
const makeFlagShorts = "eiknqswBdprR"

func classifyMake(args []string) Ruling {
	file := ""
	sawTouch := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg[2:], "=")
			switch name {
			case "file", "makefile":
				if !attached && index+1 < len(args) {
					value = args[index+1]
					index++
				}
				file = value
			case "eval":

				return Dangerous("make --eval 内联执行代码")
			case "directory", "include-dir", "jobs", "load-average", "old-file", "assume-old", "what-if", "assume-new":
				if !attached {
					index++
				}
			case "touch":
				sawTouch = true
			case "just-print", "dry-run", "recon", "question", "no-print-directory", "warn-undefined-variables", "no-builtin-rules", "no-builtin-variables", "environment-overrides", "ignore-errors", "keep-going", "silent", "quiet", "always-make", "print-directory", "debug", "print-data-base", "trace", "help", "version":
			default:
				return Indeterminate("make 包含无法识别的长选项")
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			letters := arg[1:]
			for len(letters) > 0 {
				letter := letters[0]
				switch {
				case letter == 'f':
					value := letters[1:]
					if value == "" && index+1 < len(args) {
						value = args[index+1]
						index++
					}
					file = value
					letters = ""
				case letter == 't':
					sawTouch = true
					letters = letters[1:]
				case strings.IndexByte(makeValueShorts, letter) >= 0:
					if len(letters) == 1 && index+1 < len(args) {
						index++
					}
					letters = ""
				case strings.IndexByte(makeFlagShorts, letter) >= 0:
					letters = letters[1:]
				default:
					return Indeterminate("make 包含无法识别的选项")
				}
			}
			continue
		}

	}
	if file != "" {
		if sawTouch {
			return Confirm(KindWriteFS, "make -t 触摸文件代替执行配方")
		}
		return Confirm(KindUnknown, "执行 Makefile 配方")
	}
	return Dangerous("make 执行 Makefile 配方，内容无法验证")
}

func classifyNinja(args []string) Ruling {
	file := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			switch arg {
			case "-f", "--file":
				if index+1 < len(args) {
					file = args[index+1]
					index++
				}
			case "-t", "--tool":
				if index+1 < len(args) {
					if args[index+1] == "clean" {
						return Confirm(KindWriteFS, "ninja -t clean 删除构建产物")
					}
					return Confirm(KindUnknown, "ninja 子工具执行")
				}
			case "-C", "-j", "-l", "-k", "-d", "-w":
				index++
			case "-n", "--dry-run", "-v", "--verbose", "--version":
			default:
				return Indeterminate("ninja 包含无法识别的选项")
			}
			continue
		}

	}
	if file != "" {
		return Confirm(KindUnknown, "执行 ninja 构建规则")
	}
	return Dangerous("ninja 执行 build.ninja 规则，内容无法验证")
}

func (c *classifier) classifyRemoteTransfer(name string, args []string) Ruling {
	result := Confirm(KindUnknown, "远程传输或执行需要确认")
	external := func(kind, value string) Ruling {
		return Worst(Confirm(KindUnknown, kind), c.classifyText(value))
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		lower := strings.ToLower(arg)
		switch {
		case name != "rsync" && arg == "-o":
			if index+1 < len(args) {
				result = Worst(result, c.classifySSHConfigValue(args[index+1]))
				index++
			}
		case name != "rsync" && strings.HasPrefix(arg, "-o") && len(arg) > 2:
			result = Worst(result, c.classifySSHConfigValue(arg[2:]))
		case name == "scp" && (arg == "-S" || strings.HasPrefix(arg, "-S") && len(arg) > 2):
			value := arg[2:]
			if value == "" && index+1 < len(args) {
				value = args[index+1]
				index++
			}
			if value != "" {
				result = Worst(result, external("scp 替换传输程序", value))
			}
		case name == "sftp" && (arg == "-b" || strings.HasPrefix(arg, "-b") && len(arg) > 2):
			return Dangerous("sftp 执行批处理文件，内容无法验证")
		case name == "sftp" && (arg == "-D" || strings.HasPrefix(arg, "-D") && len(arg) > 2):
			value := arg[2:]
			if value == "" && index+1 < len(args) {
				value = args[index+1]
				index++
			}
			if value != "" {
				result = Worst(result, external("sftp 直接执行服务器程序", value))
			}
		case name == "rsync" && (arg == "-e" || lower == "--rsh"):
			if index+1 < len(args) {
				result = Worst(result, external("rsync 执行传输 shell", args[index+1]))
				index++
			}
		case name == "rsync" && strings.HasPrefix(arg, "-e") && len(arg) > 2:
			result = Worst(result, external("rsync 执行传输 shell", arg[2:]))
		case name == "rsync" && strings.HasPrefix(lower, "--rsh="):
			result = Worst(result, external("rsync 执行传输 shell", arg[len("--rsh="):]))
		case name == "rsync" && lower == "--rsync-path":
			if index+1 < len(args) {
				result = Worst(result, external("rsync 执行远端程序", args[index+1]))
				index++
			}
		case name == "rsync" && strings.HasPrefix(lower, "--rsync-path="):
			result = Worst(result, external("rsync 执行远端程序", arg[len("--rsync-path="):]))
		}
	}
	return result
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
					return Indeterminate("systemctl 包含无法识别的全局选项")
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
					return Indeterminate("systemctl 包含无法识别的全局选项")
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

var dockerValueGlobals = []string{"host", "config", "context", "log-level", "tlscacert", "tlscert", "tlskey"}

var dockerFlagGlobals = []string{"version", "help"}

func dockerGlobalEnd(args []string) (int, bool) {
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg[2:], "=")
			switch {
			case containsString(dockerValueGlobals, name):
				if !attached {
					index++
				}
			case containsString(dockerFlagGlobals, name):
			default:
				return index, false
			}
			index++
			continue
		}
		if arg == "-H" {
			index += 2
			continue
		}
		return index, false
	}
	return index, true
}

func (c *classifier) classifyDocker(args []string, stdin pipeInput) Ruling {
	if len(args) == 0 {
		return Confirm(KindUnknown, "docker 子命令不明确")
	}
	index, ok := dockerGlobalEnd(args)
	if !ok {
		return Indeterminate("docker 包含无法识别的全局选项")
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
		if commandIndex, ok := dockerExecCommandIndex(rest); !ok {
			return Worst(result, Indeterminate("docker exec 包含无法识别的选项"))
		} else if commandIndex < len(rest) {
			result = Worst(result, c.child().classifyArgv(rest[commandIndex:], stdin))
		}
		return result
	case "run", "create", "start", "stop", "restart", "rm", "kill", "pause", "unpause", "rename", "update", "cp", "commit", "import", "load", "pull", "push", "build", "tag", "rmi", "save", "attach":
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
			return c.classifyDocker(append([]string{nested}, rest[1:]...), stdin)
		default:
			return Confirm(KindDockerMutate, "Docker 资源变更")
		}
	default:
		return Confirm(KindUnknown, "未知 Docker 子命令")
	}
}

func dockerExecCommandIndex(args []string) (int, bool) {
	index := 0
	sawContainer := false
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if !sawContainer {
				sawContainer = true
				index++
				continue
			}
			break
		}
		index++
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg[2:], "=")
			switch name {
			case "env", "env-file", "user", "workdir", "detach-keys":
				if !attached {
					index++
				}
			case "detach", "interactive", "tty", "privileged":
			default:
				return index, false
			}
			continue
		}
		letters := arg[1:]
		for len(letters) > 0 {
			switch letters[0] {
			case 'e', 'u', 'w':
				if len(letters) == 1 {
					index++
				}
				letters = ""
			case 'd', 'i', 't':
				letters = letters[1:]
			default:
				return index, false
			}
		}
	}
	if !sawContainer {
		return index, false
	}
	return index, true
}

var kubectlValueGlobals = []string{"as", "as-group", "as-uid", "cache-dir", "certificate-authority", "client-certificate", "client-key", "cluster", "context", "kubeconfig", "namespace", "password", "profile", "profile-output", "request-timeout", "server", "tls-server-name", "token", "user", "username", "log-backtrace-at", "log-dir", "log-file", "log-file-max-size", "log-flush-frequency", "vmodule", "v"}

var kubectlFlagGlobals = []string{"disable-compression", "insecure-skip-tls-verify", "match-server-version", "warnings-as-errors", "add-dir-header", "alsologtostderr", "logtostderr", "one-output", "skip-headers", "skip-log-headers"}

func kubectlGlobalEnd(args []string) (int, bool) {
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg[2:], "=")
			switch {
			case containsString(kubectlValueGlobals, name):
				if !attached {
					index++
				}
			case containsString(kubectlFlagGlobals, name):
			default:
				return index, false
			}
			index++
			continue
		}
		letters := arg[1:]
		for len(letters) > 0 {
			switch letters[0] {
			case 'n', 's', 'v':
				if len(letters) == 1 {
					index++
				}
				letters = ""
			default:
				return index, false
			}
		}
		index++
	}
	return index, true
}

func kubectlExecLongOption(name string) (gnuLongOption, bool) {
	for _, option := range kubectlExecLongOptions {
		if option.name == name {
			return option, true
		}
	}
	return gnuLongOption{}, false
}

func kubectlExecOptions(args []string, index int) (int, bool) {
	for index < len(args) && strings.HasPrefix(args[index], "-") && args[index] != "-" {
		arg := args[index]
		if arg == "--" {
			return index, true
		}
		if strings.HasPrefix(arg, "--") {
			name, _, attached := strings.Cut(arg[2:], "=")
			option, ok := kubectlExecLongOption(name)
			if !ok {
				return index, false
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
				return index, false
			}
		}
		index++
	}
	return index, true
}

func (c *classifier) classifyKubectl(args []string, stdin pipeInput) Ruling {
	index, ok := kubectlGlobalEnd(args)
	if !ok {
		return Indeterminate("kubectl 包含无法识别的全局选项")
	}
	args = args[index:]
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
			next, ok := kubectlExecOptions(args, index)
			if !ok {
				return Indeterminate("kubectl exec 包含无法识别的选项")
			}
			index = next
			doubleDash := false
			if index < len(args) && args[index] == "--" {
				doubleDash = true
				index++
			}
			if index >= len(args) {
				return Confirm(KindService, "Kubernetes 资源或工作负载变更")
			}
			index++
			if !doubleDash {
				next, ok = kubectlExecOptions(args, index)
				if !ok {
					return Indeterminate("kubectl exec 包含无法识别的选项")
				}
				index = next
				if index < len(args) && args[index] == "--" {
					index++
				}
			}
			if index < len(args) {

				return Worst(Confirm(KindService, "Kubernetes 资源或工作负载变更"), c.child().classifyArgv(args[index:], stdin))
			}
			return Confirm(KindService, "Kubernetes 资源或工作负载变更")
		case "delete", "drain", "cordon", "uncordon", "apply", "create", "replace", "patch", "scale", "rollout", "port-forward", "cp", "edit", "set", "label", "annotate", "taint":
			return Confirm(KindService, "Kubernetes 资源或工作负载变更")
		}
	}
	return Confirm(KindUnknown, "未知 kubectl 子命令")
}

func (c *classifier) classifyGit(args []string) Ruling {
	if len(args) == 0 {
		return Allow()
	}
	index := 0
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if arg == "-C" || arg == "-c" {
			if index+1 < len(args) {
				if arg == "-c" {
					if ruling, bad := c.gitConfigInjection(args[index+1]); bad {
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
			if ruling, bad := c.gitConfigInjection(arg[2:]); bad {
				return ruling
			}
			index++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg[2:], "=")
			if name == "exec-path" {

				return Dangerous("git --exec-path 重定向子命令执行")
			}
			if name == "config-env" {

				if !attached {
					index++
					if index < len(args) {
						value = args[index]
					}
				}
				if ruling, bad := c.gitConfigInjection(value); bad {
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
			return Indeterminate("git 包含无法识别的全局选项")
		}
		if strings.HasPrefix(arg, "-") {
			if containsString(gitFlagGlobals, arg) {
				index++
				continue
			}
			return Indeterminate("git 包含无法识别的全局选项")
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

var gitFlagGlobals = []string{"-p", "-P", "paginate", "no-pager", "bare", "version", "help", "no-replace-objects", "literal-pathspecs", "glob-pathspecs", "noglob-pathspecs", "icase-pathspecs", "no-optional-locks"}

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

func (c *classifier) gitConfigInjection(value string) (Ruling, bool) {
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
			return Worst(Dangerous("git -c 注入可执行配置"), c.classifyText(command)), true
		}
	}
	return Dangerous("git -c 注入可执行配置"), true
}

const curlValueShorts = "odFTDcKQXAbeHuUxmYyzw"

func classifyDownload(name string, args []string) Ruling {
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
		if arg == "-D" || arg == "--dump-header" || arg == "--trace" || arg == "--trace-ascii" || strings.HasPrefix(arg, "-D") && len(arg) > 2 && arg != "-D-" || strings.HasPrefix(lower, "--dump-header=") || strings.HasPrefix(lower, "--trace=") || strings.HasPrefix(lower, "--trace-ascii=") {
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
		case 'o', 'O', 'D', 'c':
			return value != "-", false
		case 'F', 'T':

			return true, false
		case 'd':

			return !getMode, false
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

const tarValueShorts = "fbCVFXTKNgI"

var tarLongOptions = []gnuLongOption{
	{name: "to-command", takesValue: true},
	{name: "use-compress-program", takesValue: true},
	{name: "checkpoint-action", takesValue: true},
	{name: "info-script", takesValue: true},
	{name: "file", takesValue: true},
	{name: "files-from", takesValue: true},
	{name: "directory", takesValue: true},
	{name: "exclude", takesValue: true},
	{name: "exclude-from", takesValue: true},
	{name: "transform", takesValue: true},
	{name: "strip-components", takesValue: true},
	{name: "occurrence", takesValue: true},
	{name: "mode", takesValue: true},
	{name: "owner", takesValue: true},
	{name: "group", takesValue: true},
	{name: "mtime", takesValue: true},
	{name: "newer", takesValue: true},
	{name: "newer-mtime", takesValue: true},
	{name: "newer-ctime", takesValue: true},
	{name: "format", takesValue: true},
	{name: "label", takesValue: true},
	{name: "listed-incremental", takesValue: true},
	{name: "index-file", takesValue: true},
	{name: "suffix", takesValue: true},
	{name: "record-size", takesValue: true},
	{name: "tapesize", takesValue: true},
	{name: "starting-file", takesValue: true},
	{name: "time", takesValue: true},
	{name: "pax-option", takesValue: true},
	{name: "quote-chars", takesValue: true},
	{name: "quote-style", takesValue: true},
	{name: "rmt-command", takesValue: true},
	{name: "rsh-command", takesValue: true},
	{name: "sparse-version", takesValue: true},
	{name: "volno-file", takesValue: true},
	{name: "xattrs-exclude", takesValue: true},
	{name: "xattrs-include", takesValue: true},
	{name: "backup", optionalValue: true},
	{name: "checkpoint", optionalValue: true},
	{name: "list"},
	{name: "extract"},
	{name: "create"},
	{name: "append"},
	{name: "update"},
	{name: "delete"},
	{name: "diff"},
	{name: "compare"},
	{name: "concatenate"},
	{name: "get"},
	{name: "verbose"},
	{name: "gzip"},
	{name: "gunzip"},
	{name: "bzip2"},
	{name: "xz"},
	{name: "lzma"},
	{name: "lzip"},
	{name: "lzop"},
	{name: "zstd"},
	{name: "compress"},
	{name: "auto-compress"},
	{name: "null"},
	{name: "to-stdout"},
	{name: "keep-old-files"},
	{name: "keep-newer-files"},
	{name: "overwrite"},
	{name: "overwrite-dir"},
	{name: "no-overwrite-dir"},
	{name: "no-recursion"},
	{name: "one-file-system"},
	{name: "absolute-names"},
	{name: "anchored"},
	{name: "no-anchored"},
	{name: "ignore-case"},
	{name: "no-ignore-case"},
	{name: "ignore-zeros"},
	{name: "ignore-failed-read"},
	{name: "ignore-command-error"},
	{name: "check-device"},
	{name: "no-check-device"},
	{name: "wildcards"},
	{name: "no-wildcards"},
	{name: "wildcards-match-slash"},
	{name: "no-wildcards-match-slash"},
	{name: "exclude-vcs"},
	{name: "exclude-vcs-ignores"},
	{name: "exclude-backups"},
	{name: "exclude-caches"},
	{name: "exclude-caches-all"},
	{name: "exclude-caches-under"},
	{name: "same-owner"},
	{name: "no-same-owner"},
	{name: "numeric-owner"},
	{name: "preserve-permissions"},
	{name: "same-permissions"},
	{name: "preserve-order"},
	{name: "preserve"},
	{name: "xattrs"},
	{name: "force-local"},
	{name: "multi-volume"},
	{name: "interactive"},
	{name: "keep-directory-symlink"},
	{name: "sparse"},
	{name: "show-defaults"},
	{name: "show-omitted-dirs"},
	{name: "show-transformed-names"},
	{name: "utc"},
	{name: "verify"},
	{name: "version"},
	{name: "help"},
}

var tarExecutingOptions = map[string]bool{
	"to-command": true, "use-compress-program": true, "checkpoint-action": true, "info-script": true,
}

func (c *classifier) classifyTar(args []string) Ruling {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			continue
		}
		option, value, attached, ok := resolveGNULongOption(arg, tarLongOptions)
		if !ok {
			return Indeterminate("tar 包含无法识别的长选项")
		}
		if tarExecutingOptions[option.name] {
			if !attached && option.takesValue && index+1 < len(args) {
				value = args[index+1]
			}
			if value == "" {
				return Indeterminate("tar 外部程序选项缺少值")
			}
			return Worst(Confirm(KindUnknown, "tar 包含外部命令执行"), c.classifyTarAction(value))
		}
		if option.takesValue && !attached && !option.optionalValue {
			index++
		}
	}

	for index := 0; index < len(args); index++ {
		arg := args[index]
		if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
			continue
		}
		letters := arg[1:]
		for len(letters) > 0 {
			letter := letters[0]
			if letter == 'I' || letter == 'F' {
				value := letters[1:]
				if value == "" && index+1 < len(args) {
					value = args[index+1]
				}
				if value == "" {
					return Indeterminate("tar -" + string(letter) + " 缺少外部程序")
				}
				return Worst(Confirm(KindUnknown, "tar 包含外部命令执行"), c.classifyTarAction(value))
			}
			if strings.IndexByte(tarValueShorts, letter) >= 0 {

				letters = ""
				continue
			}
			letters = letters[1:]
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
}

func (c *classifier) classifyTarAction(value string) Ruling {
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "exec=") {
		return c.classifyText(strings.TrimSpace(value[len("exec="):]))
	}
	if !strings.Contains(lower, "=") {
		return c.classifyText(value)
	}
	return Allow()
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
				return Indeterminate("redis-cli 包含无法识别的选项")
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
