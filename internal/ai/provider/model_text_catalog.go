package provider

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type modelTextTemplateSpec struct {
	source string
	target string
}

type modelTextTemplate struct {
	source  string
	target  string
	matcher *regexp.Regexp
	tokens  []modelTextToken
	nested  []byte
}

type modelTextToken struct {
	literal  string
	argument int
	verb     byte
}

var modelTextTemplates = compileModelTextTemplates()

func compileModelTextTemplates() []modelTextTemplate {
	result := make([]modelTextTemplate, 0, len(modelTextTemplateSpecs))
	for _, spec := range modelTextTemplateSpecs {
		sourceTokens, err := parseModelTextFormat(spec.source)
		if err != nil {
			panic(err)
		}
		targetTokens, err := parseModelTextFormat(spec.target)
		if err != nil {
			panic(err)
		}
		var pattern strings.Builder
		pattern.WriteString("^")
		nested := make([]byte, 0, len(sourceTokens))
		argument := 0
		for _, token := range sourceTokens {
			pattern.WriteString(regexp.QuoteMeta(token.literal))
			if token.verb == 0 {
				continue
			}
			if token.argument != argument {
				panic(fmt.Sprintf("non-sequential model text source argument in %q", spec.source))
			}
			argument++
			mode := byte(0)
			switch token.verb {
			case 'v', 'w':
				mode = 'w'
			case 'e':
				mode = 'e'
			}
			nested = append(nested, mode)
			switch token.verb {
			case 'd':
				pattern.WriteString(`(-?[0-9]+)`)
			case 'q':
				pattern.WriteString(`("(?:\\.|[^"\\])*")`)
			default:
				pattern.WriteString(`((?s:.*?))`)
			}
		}
		pattern.WriteString("$")
		result = append(result, modelTextTemplate{
			source: spec.source, target: spec.target,
			matcher: regexp.MustCompile(pattern.String()), tokens: targetTokens, nested: nested,
		})
	}
	return result
}

func parseModelTextFormat(format string) ([]modelTextToken, error) {
	var tokens []modelTextToken
	literal := strings.Builder{}
	argument := 0
	flush := func(verb byte, index int) {
		tokens = append(tokens, modelTextToken{literal: literal.String(), argument: index, verb: verb})
		literal.Reset()
	}
	for index := 0; index < len(format); index++ {
		if format[index] != '%' {
			literal.WriteByte(format[index])
			continue
		}
		index++
		if index >= len(format) {
			return nil, fmt.Errorf("unterminated model text format %q", format)
		}
		if format[index] == '%' {
			literal.WriteByte('%')
			continue
		}
		nextArgument := argument
		if format[index] == '[' {
			end := strings.IndexByte(format[index:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated argument index in %q", format)
			}
			end += index
			parsed, err := strconv.Atoi(format[index+1 : end])
			if err != nil || parsed < 1 {
				return nil, fmt.Errorf("invalid argument index in %q", format)
			}
			nextArgument = parsed - 1
			index = end + 1
			if index >= len(format) {
				return nil, fmt.Errorf("missing verb in %q", format)
			}
		}
		verb := format[index]
		if !strings.ContainsRune("sqdvwe", rune(verb)) {
			return nil, fmt.Errorf("unsupported verb in %q", format)
		}
		flush(verb, nextArgument)
		argument = nextArgument + 1
	}
	flush(0, 0)
	return tokens, nil
}

func projectModelResult(view modelResultView) string {
	if view.ModelText != "" {
		return projectModelTextWithAuditSuffix(view)
	}
	switch view.Tool {
	case "exec_commands", "docker_exec", "db_query":
		if core, suffix, ok := splitModelAuditSuffix(view.Text, 0); ok {
			rawSuffix := view.Text[len(core):]
			rawDetail := rawSuffix[len("\n[结果审计写入失败: ") : len(rawSuffix)-1]
			if projectMachineTextDepth(rawDetail, 1) == rawDetail {
				suffix = rawSuffix
			}
			view.Text = core
			return projectModelResult(view) + suffix
		}
	}
	switch view.Tool {
	case "done":
		return view.Text
	case "spawn_subagent":
		if view.Panic {
			return projectMachineText(view.Text)
		}
		return view.Text
	case "read_file", "list_dir", "search_files", "db_list_tables", "db_describe", "redis_scan", "todo_write":
		if view.OK {
			return projectFreeResultText(view.Text, view.Truncated)
		}
	case "list_assets":
		if view.OK {
			if view.Text == "没有资产" {
				return "no assets"
			}
			return projectFreeResultText(view.Text, view.Truncated)
		}
	case "docker_ps", "docker_logs":
		if view.OK {
			if view.Tool == "docker_ps" && view.Text == "没有容器" {
				return "no containers"
			}
			return projectFreeResultText(view.Text, view.Truncated)
		}
	case "docker_exec":
		if strings.HasPrefix(view.Text, "工具参数无效: ") || view.Text == "Docker 服务未配置" {
			return projectMachineText(view.Text)
		}
		return projectFreeResultText(view.Text, view.Truncated)
	case "db_query":
		if view.OK {
			return projectDatabaseQueryText(view.Text, view.Truncated)
		}
	case "exec_commands":
		if view.OK {
			return view.Text
		}
	case "read_screen":
		if view.OK {
			if projected, ok := projectReadScreenText(view.Text); ok {
				return projected
			}
			return view.Text
		}
	case "send_keys", "wait_for":
		if projected, ok := projectTerminalWaitText(view.Text); ok {
			return projected
		}
	case "terminal_create":
		if view.OK {
			if projected, ok := projectTerminalCreateText(view.Text); ok {
				return projected
			}
		}
	case "shell_history":
		if view.OK {
			return projectShellHistoryText(view.Text, view.Truncated)
		}
	case "memory_list":
		if view.OK {
			return projectMemoryListText(view.Text)
		}
	case "memory_recall":
		if view.OK {
			return view.Text
		}
	}
	return projectMachineText(view.Text)
}

func projectModelTextWithAuditSuffix(view modelResultView) string {
	core, suffix, ok := splitModelAuditSuffix(view.Text, 0)
	if !ok {
		return view.ModelText
	}
	rawSuffix := view.Text[len(core):]
	modelText := view.ModelText
	if strings.HasSuffix(modelText, suffix) {
		return modelText
	}
	if strings.HasSuffix(modelText, rawSuffix) {
		return strings.TrimSuffix(modelText, rawSuffix) + suffix
	}
	return modelText
}

func splitModelAuditSuffix(text string, depth int) (string, string, bool) {
	const marker = "\n[结果审计写入失败: "
	if !strings.HasSuffix(text, "]") {
		return text, "", false
	}
	index := strings.LastIndex(text, marker)
	if index < 0 {
		return text, "", false
	}
	nested := text[index+len(marker) : len(text)-1]
	return text[:index], "\n[audit write failed: " + projectMachineTextDepth(nested, depth+1) + "]", true
}

func projectFreeResultText(text string, truncated bool) string {
	if !truncated {
		return text
	}
	if strings.HasSuffix(text, "\n[输出已截断]") {
		return strings.TrimSuffix(text, "\n[输出已截断]") + "\n[output truncated]"
	}
	return text
}

func projectDatabaseQueryText(text string, truncated bool) string {
	if truncated {
		core := text
		suffix := ""
		if strings.HasSuffix(core, "\n[输出已截断]") {
			core = strings.TrimSuffix(core, "\n[输出已截断]")
			suffix = "\n[输出已截断]"
		}
		footer := regexp.MustCompile(`仅显示前 [0-9]+ 行（查询返回 [0-9]+ 行）\n$`).FindString(core)
		if footer != "" {
			core = strings.TrimSuffix(core, footer) + projectMachineText(footer)
		}
		text = core + suffix
	}
	return projectFreeResultText(text, truncated)
}

func projectMachineText(text string) string {
	return projectMachineTextDepth(text, 0)
}

func projectMachineTextDepth(text string, depth int) string {
	if depth > 16 {
		return text
	}
	core, suffix := splitModelMachineSuffix(text, depth)
	if translated, ok := translateModelMachineText(core, depth); ok {
		core = translated
	}
	return core + suffix
}

func splitModelMachineSuffix(text string, depth int) (string, string) {
	core := text
	suffix := ""
	for {
		if strings.HasSuffix(core, "\n[输出已截断]") {
			core = strings.TrimSuffix(core, "\n[输出已截断]")
			suffix = "\n[output truncated]" + suffix
			continue
		}
		if strings.HasSuffix(core, "（原始写入结果不确定，未重试）") {
			core = strings.TrimSuffix(core, "（原始写入结果不确定，未重试）")
			suffix = "(original write result was uncertain; write was not retried)" + suffix
			continue
		}
		if strings.HasSuffix(core, "]") {
			if marker := strings.LastIndex(core, "\n[结果审计写入失败: "); marker >= 0 {
				nested := core[marker+len("\n[结果审计写入失败: ") : len(core)-1]
				suffix = "\n[audit write failed: " + projectMachineTextDepth(nested, depth+1) + "]" + suffix
				core = core[:marker]
				continue
			}
		}
		return core, suffix
	}
}

func translateModelMachineText(text string, depth int) (string, bool) {
	if translated, ok := modelStaticText[text]; ok {
		return translated, true
	}
	if translated, ok := modelAdditionalStaticText[text]; ok {
		return translated, true
	}
	for _, template := range modelTextTemplates {
		matches := template.matcher.FindStringSubmatch(text)
		if matches == nil {
			continue
		}
		var output strings.Builder
		for _, token := range template.tokens {
			output.WriteString(token.literal)
			if token.verb == 0 {
				continue
			}
			value := matches[token.argument+1]
			if token.argument < len(template.nested) {
				switch template.nested[token.argument] {
				case 'w':
					value = projectMachineTextDepth(value, depth+1)
				case 'e':
					value = projectModelErrorDetail(value, depth+1)
				}
			}
			output.WriteString(value)
		}
		return output.String(), true
	}
	return text, false
}

func projectModelErrorDetail(text string, depth int) string {
	if strings.HasPrefix(text, "内部错误: ") || strings.HasPrefix(text, "参数错误: ") {
		return projectMachineTextDepth(text, depth)
	}
	return text
}

var (
	readScreenHeaderPattern = regexp.MustCompile(`^光标 \((-?[0-9]+), (-?[0-9]+)\)，空闲 (-?[0-9]+)ms，屏幕序号 (-?[0-9]+)(，AltScreen)?\n`)
	takeoverHeaderPattern   = regexp.MustCompile(`^光标 \((-?[0-9]+), (-?[0-9]+)\)，空闲 (-?[0-9]+)ms\n`)
	commandStatePattern     = regexp.MustCompile(`^命令状态：(空闲|运行中)，序号 ([0-9]+)(?:，上次退出码 (-?[0-9]+))?\n`)
	newOutputHeaderPattern  = regexp.MustCompile(`^新输出（序号 ([0-9]+) → ([0-9]+)，最新 ([0-9]+)）([^\n]*)\n`)
	commandTailPattern      = regexp.MustCompile(`^命令序号 ([0-9]+)，屏幕序号 ([0-9]+)\n屏幕尾部：\n`)
)

func projectReadScreenText(text string) (string, bool) {
	if matches := readScreenHeaderPattern.FindStringSubmatch(text); matches != nil {
		alt := ""
		if matches[5] != "" {
			alt = ", AltScreen"
		}
		header := fmt.Sprintf("cursor (%s, %s), idle %sms, screen sequence %s%s\n", matches[1], matches[2], matches[3], matches[4], alt)
		rest := text[len(matches[0]):]
		if state := commandStatePattern.FindStringSubmatch(rest); state != nil {
			status := "idle"
			if state[1] == "运行中" {
				status = "running"
			}
			header += fmt.Sprintf("command state: %s, sequence %s", status, state[2])
			if state[3] != "" {
				header += fmt.Sprintf(", last exit code %s", state[3])
			}
			header += "\n"
			rest = rest[len(state[0]):]
		}
		return header + rest, true
	}
	if matches := takeoverHeaderPattern.FindStringSubmatch(text); matches != nil {
		return fmt.Sprintf("cursor (%s, %s), idle %sms\n", matches[1], matches[2], matches[3]) + text[len(matches[0]):], true
	}
	if matches := newOutputHeaderPattern.FindStringSubmatch(text); matches != nil {
		header := fmt.Sprintf("new output (sequence %s -> %s, latest %s)", matches[1], matches[2], matches[3])
		notes := matches[4]
		notes = strings.ReplaceAll(notes, "；锚点已过期，改为读取最早保留的输出", "; anchor expired, reading the oldest retained output instead")
		notes = regexp.MustCompile(`；还有未读输出，下一页 since_seq=([0-9]+)`).ReplaceAllString(notes, "; more unread output, next page since_seq=$1")
		notes = regexp.MustCompile(`；本页渲染已截断，用 since_seq=([0-9]+) 配合更小 max_bytes 重读`).ReplaceAllString(notes, "; this page was truncated during rendering, reread with since_seq=$1 and a smaller max_bytes")
		return header + notes + "\n" + text[len(matches[0]):], true
	}
	return text, false
}

func projectTerminalWaitText(text string) (string, bool) {
	if strings.HasPrefix(text, "等待超时，最近输出：\n") {
		return "wait timed out, recent output:\n" + strings.TrimPrefix(text, "等待超时，最近输出：\n"), true
	}
	if matches := regexp.MustCompile(`^等待超时，锚点 ([0-9]+) 之后的新输出(（部分输出已被淘汰）)?：\n`).FindStringSubmatch(text); matches != nil {
		evicted := ""
		if matches[2] != "" {
			evicted = " (some output was evicted)"
		}
		return fmt.Sprintf("wait timed out, new output after anchor %s%s:\n", matches[1], evicted) + text[len(matches[0]):], true
	}
	if marker := strings.Index(text, "\n命令序号 "); marker >= 0 {
		headText := text[:marker]
		head := ""
		if matches := regexp.MustCompile(`^(.*)，退出码 (-?[0-9]+)$`).FindStringSubmatch(headText); matches != nil {
			head = projectMachineText(matches[1]) + ", exit code " + matches[2]
		} else {
			head = projectMachineText(headText)
		}
		tail := text[marker+1:]
		if matches := commandTailPattern.FindStringSubmatch(tail); matches != nil {
			header := fmt.Sprintf("command sequence %s, screen sequence %s\nscreen tail:\n", matches[1], matches[2])
			return head + "\n" + header + tail[len(matches[0]):], true
		}
	}
	return text, false
}

func projectTerminalCreateText(text string) (string, bool) {
	views := []struct {
		prefix string
		suffix string
		format string
	}{
		{prefix: "已在后台创建分离终端（", suffix: "），AI 可在其中工作，用户可在「后台会话」中接管", format: "created detached terminal in the background (%s); AI can work in it, and the user can take over in 「后台会话」"},
		{prefix: "已创建并在前台展示终端标签页（", suffix: "），AI 正在其中工作，用户可随时接管输入", format: "created and displayed terminal tab in the foreground (%s); AI is working in it, and the user can take over input at any time"},
	}
	for _, view := range views {
		if !strings.HasPrefix(text, view.prefix) || !strings.HasSuffix(text, view.suffix) {
			continue
		}
		detail := strings.TrimSuffix(strings.TrimPrefix(text, view.prefix), view.suffix)
		return fmt.Sprintf(view.format, projectTerminalCreateDetail(detail)), true
	}
	return text, false
}

func projectTerminalCreateDetail(detail string) string {
	command := ""
	if strings.HasSuffix(detail, "，启动命令已发送") {
		detail = strings.TrimSuffix(detail, "，启动命令已发送")
		command = ", startup command sent"
	}
	if marker := strings.Index(detail, "，标题「"); marker >= 0 && strings.HasSuffix(detail, "」") {
		return detail[:marker] + ", title \"" + detail[marker+len("，标题「"):len(detail)-len("」")] + "\"" + command
	}
	return detail + command
}

func projectShellHistoryText(text string, truncated bool) string {
	body := projectFreeResultText(text, truncated)
	prefix := ""
	const remote = "[注意] 当前会话连接的是远端资产；以下是运行 NexTerm 的本机 shell 历史，不是远端机器的历史。\n"
	if strings.HasPrefix(body, remote) {
		prefix = "[note] this session is connected to a remote asset; the following is the shell history of the local machine running NexTerm, not the remote machine.\n"
		body = strings.TrimPrefix(body, remote)
	}
	empty := false
	pattern := regexp.MustCompile(`^\[本机 ([^\n]+) shell 历史，最新 ([0-9]+) 条\]\n`)
	if matches := pattern.FindStringSubmatch(body); matches != nil {
		prefix += fmt.Sprintf("[local %s shell history, newest %s entries]\n", matches[1], matches[2])
		empty = matches[2] == "0"
		body = body[len(matches[0]):]
	}
	if empty && body == "（历史为空）" {
		body = "(history is empty)"
	}
	return prefix + body
}

func projectMemoryListText(text string) string {
	if translated, ok := translateModelMachineText(text, 0); ok {
		return translated
	}
	lines := strings.SplitAfter(text, "\n")
	for index, line := range lines {
		newline := ""
		content := strings.TrimSuffix(line, "\n")
		if content != line {
			newline = "\n"
		}
		content = strings.TrimSuffix(content, " [已脱敏]")
		if content != strings.TrimSuffix(line, "\n") {
			content += " [redacted]"
		}
		if translated, ok := translateModelMachineText(content, 0); ok {
			content = translated
		}
		lines[index] = content + newline
	}
	return strings.Join(lines, "")
}

var modelTextTemplateSpecs = []modelTextTemplateSpec{
	{source: "工具参数无效: %w", target: "invalid tool arguments: %w"},
	{source: "权限策略已拒绝: %w", target: "permission policy denied: %w"},
	{source: "用户回答：%s", target: "user answer: %s"},
	{source: "未知参数 %s", target: "unknown argument %s"},
	{source: "缺少必填参数 %s", target: "missing required argument %s"},
	{source: "参数 %s 应为 %s", target: "argument %s must be %s"},
	{source: "NexTerm 没有 %s 的规则，无法确定它是否会修改系统", target: "NexTerm has no rule for %s, so it cannot tell whether it changes the system"},
	{source: "%s 的这个子命令没有对应规则", target: "there is no rule for this %s subcommand"},
	{source: "参数 %s 的值 %q 不在允许范围", target: "value %[2]q of argument %[1]s is not allowed"},
	{source: "未知工具 %s", target: "unknown tool %s"},
	{source: "相同调用正在执行中，未重复执行: %w", target: "the same call is already running; did not execute it again: %w"},
	{source: "调用与已记录的执行冲突（授权或参数不一致）: %w", target: "call conflicts with the recorded execution because authorization or arguments differ: %w"},
	{source: "执行结果记录失败: %w", target: "failed to record execution result: %w"},
	{source: "操作已执行，但结果记录失败：%w", target: "operation was executed, but recording its result failed: %w"},
	{source: "调用已执行过，返回已记录的结果（outcome=%s）", target: "call was already executed; returning the recorded result (outcome=%s)"},
	{source: "调用已执行过，返回已记录的结果（outcome=%s）: %w", target: "call was already executed; returning the recorded result (outcome=%s): %w"},

	{source: "old_string 匹配 %d 处；需要恰好 1 处或启用 replace_all", target: "old_string matched %d locations; exactly 1 match is required unless replace_all is enabled"},
	{source: "当前文件服务不支持安全条件创建: %w", target: "current file service does not support safe conditional creation: %w"},
	{source: "%w；解析文件服务以确认结果失败: %v", target: "%w; failed to resolve the file service to confirm the result: %v"},
	{source: "%w；读取目标以确认结果失败: %v", target: "%w; failed to read the target to confirm the result: %v"},
	{source: "%w；目标内容与本次写入不一致，未重试", target: "%w; target content differs from this write, so the write was not retried"},
	{source: "已创建 %s（%d 字节，原子写入）", target: "created %s (%d bytes, atomic write)"},
	{source: "已编辑 %s（替换 %d 处，已备份原文件；非事务覆盖，可能覆盖确认后的外部修改）", target: "edited %s (replaced %d locations, original file backed up; non-transactional overwrite may overwrite external changes made after confirmation)"},
	{source: "已写入 %s（%d 字节，已备份原文件；非事务覆盖，可能覆盖确认后的外部修改）", target: "wrote %s (%d bytes, original file backed up; non-transactional overwrite may overwrite external changes made after confirmation)"},
	{source: "仅显示前 %d 行（查询返回 %d 行）\n", target: "showing only the first %d rows (query returned %d rows)\n"},
	{source: "数据库连接 %s 不存在或已关闭", target: "database connection %s does not exist or is closed"},
	{source: "参数错误: %w", target: "invalid parameters: %w"},
	{source: "MySQL 配置无效: %e", target: "invalid MySQL configuration: %e"},
	{source: "PostgreSQL 配置无效: %e", target: "invalid PostgreSQL configuration: %e"},
	{source: "不支持的数据库类型 %s", target: "unsupported database type %s"},
	{source: "数据库资产配置无效: %e", target: "invalid database asset configuration: %e"},
	{source: "SQL 执行超时（%s）", target: "SQL execution timed out (%s)"},
	{source: "不支持的 TLS 模式 %s", target: "unsupported TLS mode %s"},
	{source: "读取 TLS CA: %e", target: "failed to read TLS CA: %e"},
	{source: "内部错误: 读取数据库资产: %w", target: "internal error: failed to read database asset: %w"},
	{source: "内部错误: 读取数据库凭据: %w", target: "internal error: failed to read database credential: %w"},
	{source: "内部错误: %w: %e", target: "internal error: %w: %e"},
	{source: "不支持的 Docker action %s", target: "unsupported Docker action %s"},
	{source: "%s %s 完成", target: "%s %s completed"},
	{source: "通知内容过长或不是有效 UTF-8（上限 %d 字符）", target: "notification content is too long or is not valid UTF-8 (maximum %d characters)"},
	{source: "未知待办状态 %s", target: "unknown todo status %s"},
	{source: "读取 %s 历史失败: %w", target: "failed to read %s history: %w"},
	{source: "未找到本机 shell 历史文件（已尝试: %s）", target: "no local shell history file found (tried: %s)"},
	{source: "未知控制键 <%s>", target: "unknown control key <%s>"},
	{source: "工具 %s 执行时崩溃：%s", target: "tool %s panicked during execution: %s"},
	{source: "提醒内容过长或不是有效 UTF-8（上限 %d 字符）", target: "reminder content is too long or is not valid UTF-8 (maximum %d characters)"},
	{source: "delay_minutes 超过 30 天上限（%d 分钟）", target: "delay_minutes exceeds the 30-day limit (%d minutes)"},
	{source: "创建提醒失败: %w", target: "failed to create reminder: %w"},
	{source: "提醒已创建（任务 %s），将于 %s 投递到当前会话；可在定时任务列表查看投递状态", target: "reminder created (job %s) and will be delivered to this conversation at %s; delivery status is available in the scheduled jobs list"},

	{source: "文件名 glob 无效: %v", target: "invalid filename glob: %v"},
	{source: "内容正则无效: %v", target: "invalid content regex: %v"},
	{source: "正则无效: %v", target: "invalid regex: %v"},
	{source: "正则无效: %w", target: "invalid regex: %w"},
	{source: "max_bytes 必须在 1-%d 字节之间", target: "max_bytes must be between 1 and %d bytes"},
	{source: "wait_ms 必须在 0-%d 毫秒之间", target: "wait_ms must be between 0 and %d milliseconds"},
	{source: "timeout_ms 超出 1-%d 毫秒范围", target: "timeout_ms must be between 1 and %d milliseconds"},
	{source: "光标 (%d, %d)，空闲 %dms，屏幕序号 %d", target: "cursor (%d, %d), idle %dms, screen sequence %d"},
	{source: "光标 (%d, %d)，空闲 %dms\n%s", target: "cursor (%d, %d), idle %dms\n%s"},
	{source: "命令状态：%s，序号 %d", target: "command state: %s, sequence %d"},
	{source: "新输出（序号 %d → %d，最新 %d）", target: "new output (sequence %d -> %d, latest %d)"},
	{source: "已发送（屏幕序号 %d）", target: "sent (screen sequence %d)"},
	{source: "已发送（屏幕序号 %d）；该终端不支持 OSC 133 命令跟踪，无法确认命令是否完成，请用 wait_for 校验新输出", target: "sent (screen sequence %d); this terminal does not support OSC 133 command tracking, so command completion cannot be confirmed; use wait_for to verify new output"},
	{source: "模式已出现（新输出，屏幕序号 %d）", target: "pattern appeared (new output, screen sequence %d)"},
	{source: "等待超时，锚点 %d 之后的新输出", target: "wait timed out, new output after anchor %d"},
	{source: "已将终端标签页 %s 重命名为「%s」", target: "renamed terminal tab %s to \"%s\""},
	{source: "已在后台创建分离终端（%s），AI 可在其中工作，用户可在「后台会话」中接管", target: "created detached terminal in the background (%s); AI can work in it, and the user can take over in 「后台会话」"},
	{source: "已创建并在前台展示终端标签页（%s），AI 正在其中工作，用户可随时接管输入", target: "created and displayed terminal tab in the foreground (%s); AI is working in it, and the user can take over input at any time"},
	{source: "命令序号 %d，屏幕序号 %d\n屏幕尾部：\n%s", target: "command sequence %d, screen sequence %d\nscreen tail:\n%s"},

	{source: "已保存记忆 %s（主题 %s，版本 %d）", target: "saved memory %s (topic %s, version %d)"},
	{source: "[已省略 %d 条]", target: "[%d entries omitted]"},
	{source: "记忆 %s 不存在", target: "memory %s not found"},
	{source: "— %s（主题 %s，版本 %d%s）\n%s\n", target: "- %s (topic %s, version %d%s)\n%s\n"},
	{source: "[%d 条内容包含疑似密钥，已脱敏]", target: "[%d entries contained suspected secrets and were redacted]"},
	{source: "记忆 %s 版本冲突（期望 %d，实际 %d），请刷新后重试", target: "memory %s version conflict (expected %d, actual %d); refresh and try again"},
	{source: "已删除记忆 %s", target: "deleted memory %s"},

	{source: "接管任务内部错误: %v", target: "internal takeover task error: %v"},
	{source: "解析接管工具 %s 结果失败: %w", target: "failed to parse result for takeover tool %s: %w"},
	{source: "未知接管 HITL 类型 %s", target: "unknown takeover HITL type %s"},
	{source: "已达到 %d 步接管上限", target: "reached the takeover limit of %d steps"},
	{source: "无法记录执行尝试，未写入终端: %w", target: "failed to record the execution attempt; nothing was written to the terminal: %w"},
	{source: "已写入终端，但结果记录失败，请读取屏幕确认：%w", target: "input was written to the terminal, but recording the result failed; read the screen to confirm: %w"},
	{source: "无法查询会话的终端范围，本次定时运行已终止，请确认会话仍然存在后重试: %w", target: "failed to query terminal scope for the conversation; this scheduled run was aborted, verify the conversation still exists and try again: %w"},

	{source: "布局不是合法 JSON: %w", target: "layout is not valid JSON: %w"},
	{source: "创建数据库目录失败: %w", target: "failed to create database directory: %w"},
	{source: "准备数据库文件失败: %w", target: "failed to prepare database file: %w"},
	{source: "保护数据库文件失败: %w", target: "failed to protect database file: %w"},
	{source: "备份旧版数据库失败: %w", target: "failed to back up legacy database: %w"},
	{source: "数据库错误: %w", target: "database error: %w"},
	{source: "数据库迁移错误: %w", target: "database migration error: %w"},
	{source: "数据编码失败: %w", target: "failed to encode data: %w"},

	{source: "%s 包含无法识别的长选项", target: "%s contains an unrecognized long option"},
	{source: "%s 包含无法识别的选项", target: "%s contains an unrecognized option"},
	{source: "%s 执行文件中的命令，内容无法验证", target: "%s executes commands from a file whose contents cannot be verified"},
	{source: "%s 删除计划任务", target: "%s deletes scheduled tasks"},
	{source: "%s 安装计划任务", target: "%s installs scheduled tasks"},
	{source: "%s 执行标准输入中的命令", target: "%s executes commands from standard input"},
	{source: "%s 缺少要执行的命令", target: "%s is missing a command to execute"},
	{source: "%s 启动特权会话", target: "%s starts a privileged session"},
	{source: "%s 执行文件中的代码，内容无法验证", target: "%s executes code from a file whose contents cannot be verified"},
	{source: "%s 从标准输入执行代码", target: "%s executes code from standard input"},
	{source: "tar -%s 缺少外部程序", target: "tar -%s is missing an external program"},
	{source: "命令无法可靠解析: %w", target: "command cannot be parsed reliably: %w"},
}

var modelStaticText = map[string]string{
	"写入前必须先使用 read_file 成功读取目标；新文件需先用 read_file 确认不存在": "before writing, read the target successfully with read_file; for a new file, first use read_file to confirm it does not exist",
	"文件在读取或确认后已变化，请重新读取并生成差异":                          "file changed after it was read or confirmed; read it again and regenerate the diff",
	"工具参数无效":                          "invalid tool arguments",
	"终端目标不属于当前 AI 会话范围":               "terminal target is outside the current AI conversation scope",
	"终端输入在确认后已变化，请重新评估":               "terminal input changed after confirmation; evaluate it again",
	"需要用户回答":                          "user answer required",
	"Docker 服务未配置":                    "Docker service is not configured",
	"操作失败（原因未记录），请重试":                 "operation failed for an unrecorded reason; try again",
	"[输出已截断]":                         "[output truncated]",
	"JSON 后存在多余内容":                    "unexpected content after JSON",
	"参数必须是 JSON 对象":                   "arguments must be a JSON object",
	"工具输出被截断或传输失败，无法确定执行结果":           "tool output was truncated or failed to transfer; execution result is unknown",
	"path 不能为空":                       "path cannot be empty",
	"不能编辑不存在的文件":                      "cannot edit a file that does not exist",
	"不能覆盖非 UTF-8 或二进制文件":              "cannot overwrite a non-UTF-8 or binary file",
	"old_string 必须非空且与 new_string 不同": "old_string must be nonempty and different from new_string",
	"缺少写入前差异准备，请重新调用工具":               "missing pre-write diff preparation; call the tool again",
	"数据库服务未配置":                        "database service is not configured",
	"当前 AI 作用域没有 connId":              "current AI scope has no connId",
	"table 不能为空":                      "table cannot be empty",
	"sql 不能为空":                        "sql cannot be empty",
	"count 不能超过 1000":                 "count cannot exceed 1000",
	"没有容器":                            "no containers",
	"container_id 含非法字符":              "container_id contains invalid characters",
	"tail 必须在 1-5000 之间":              "tail must be between 1 and 5000",
	"container_id 或 cmd 无效":           "invalid container_id or cmd",
	"通知服务未配置":                         "notification service is not configured",
	"message 不能为空":                    "message cannot be empty",
	"应用内通知已送达；通知内容需同时写进当轮回复，弹窗可能被错过": "in-app notification delivered; also include the notification content in the current reply because the popup may be missed",
	"资产服务未配置":       "asset service is not configured",
	"没有资产":          "no assets",
	"question 不能为空": "question cannot be empty",
	"选项不能为空":        "options cannot be empty",
	"todos 最多 50 项": "todos can contain at most 50 items",
	"待办内容不能为空":      "todo content cannot be empty",
	"plan 不能为空":     "plan cannot be empty",
	"计划已提交，请等待用户审核；不会自动执行。":                          "plan submitted; wait for user review, and do not execute it automatically.",
	"limit 必须为正整数":                                   "limit must be a positive integer",
	"（历史为空）":                                         "(history is empty)",
	"无法确定本机用户目录，未读取 shell 历史":                        "cannot determine the local user home directory; shell history was not read",
	"该终端不支持锚定输出读取":                                   "this terminal does not support anchored output reads",
	"未知崩溃":                                           "unknown panic",
	"提醒内容不能为空":                                       "reminder content cannot be empty",
	"delay_minutes 与 at 只能二选一":                       "specify only one of delay_minutes and at",
	"at 必须是 RFC3339 时间（如 2026-01-02T15:04:05+08:00）": "at must be an RFC3339 timestamp, for example 2026-01-02T15:04:05+08:00",
	"delay_minutes 不能为负数":                            "delay_minutes cannot be negative",
	"必须提供 delay_minutes 或 at 之一":                     "one of delay_minutes or at is required",
	"提醒时间必须晚于当前时间":                                   "reminder time must be later than the current time",
	"提醒时间最远只能设定在 30 天内":                              "reminder time can be at most 30 days in the future",
	"当前运行缺少会话标识，无法投递提醒":                              "current run has no conversation ID, so the reminder cannot be delivered",
	"路径越界：规范化后仍包含 ..":                                "path escapes its boundary: normalized path still contains ..",
	"该操作必须逐次确认，不支持永久授权":                              "this operation requires confirmation every time and does not support persistent grants",
	"永久授权未配置":                                        "persistent grants are not configured",
	"无法确定设备身份，不能保存永久授权":                              "cannot determine device identity, so the persistent grant cannot be saved",
	"用户拒绝了此操作":                                       "user denied this operation",
	"确认结果无效，操作未执行":                                   "invalid confirmation result; operation was not executed",
	"计划模式只允许只读工具":                                    "plan mode only allows read-only tools",
	"读取本机 shell 历史文件（历史常包含敏感命令，需要确认）":                "read local shell history files (history often contains sensitive commands and requires confirmation)",
	"exit_plan_mode 仅在计划模式可用":                        "exit_plan_mode is only available in plan mode",
	"会话传输未配置":                                        "session transport is not configured",
	"当前 AI 作用域没有 sessionId":                          "current AI scope has no sessionId",
	"当前会话不支持文件操作":                                    "current session does not support file operations",
	"commands 必须包含 1-20 条命令":                         "commands must contain between 1 and 20 commands",
	"命令不能为空":                                         "command cannot be empty",
	"timeout 必须在 1-300 秒之间":                          "timeout must be between 1 and 300 seconds",
	"max_bytes 必须在 1 字节到 2 MiB 之间":                   "max_bytes must be between 1 byte and 2 MiB",
	"path/pattern 不能为空，by 必须为 name 或 content":        "path/pattern cannot be empty, and by must be name or content",
	"终端服务未配置":                                        "terminal service is not configured",
	"当前 AI 作用域没有 tabId":                              "current AI scope has no tabId",
	"已发送":                                            "sent",
	"已发送；该终端不支持 OSC 133 命令跟踪，无法确认命令是否完成，请用 wait_for 校验新输出": "sent; this terminal does not support OSC 133 command tracking, so command completion cannot be confirmed; use wait_for to verify new output",
	"命令已完成":                   "command completed",
	"等待结束，命令仍在运行":             "wait ended; command is still running",
	"等待结束，未观察到新命令开始":          "wait ended; no new command start was observed",
	"模式已出现":                   "pattern appeared",
	"连接已断开：数据库服务已关闭，请重新连接":    "disconnected: database service is closed; reconnect",
	"请选择 schema":              "select a schema",
	"未选择数据库，请先选择 schema":      "no database selected; select a schema first",
	"MySQL 连接失败":              "MySQL connection failed",
	"PostgreSQL 连接失败":         "PostgreSQL connection failed",
	"Redis 连接失败":              "Redis connection failed",
	"Redis TTL 失败":            "Redis TTL failed",
	"Redis 读取键值失败":            "failed to read Redis key value",
	"Redis 设置 TTL 失败":         "failed to set Redis TTL",
	"Redis 命令为空":              "Redis command is empty",
	"Redis 命令失败":              "Redis command failed",
	"Redis 数据库编号必须是非负整数":      "Redis database number must be a non-negative integer",
	"生成数据库连接 ID":              "failed to generate database connection ID",
	"关闭数据库连接":                 "failed to close database connection",
	"需要提供数据库资产或连接配置":          "database asset or connection configuration is required",
	"数据库资产解析器未配置":             "database asset resolver is not configured",
	"数据库端口超出范围":               "database port is out of range",
	"读取数据库资产":                 "failed to read database asset",
	"读取数据库凭据":                 "failed to read database credential",
	"资产存储未配置":                 "asset store is not configured",
	"凭据解密器未配置":                "credential decryptor is not configured",
	"查询超时时间（毫秒）超出范围":          "query timeout in milliseconds is out of range",
	"读取 PostgreSQL 表列表":       "failed to read PostgreSQL table list",
	"读取 MySQL 表列表":            "failed to read MySQL table list",
	"读取 PostgreSQL schema 列表": "failed to read PostgreSQL schema list",
	"读取 MySQL 数据库列表":          "failed to read MySQL database list",
	"读取 MySQL 字段列表":           "failed to read MySQL column list",
	"读取 MySQL 索引列表":           "failed to read MySQL index list",
	"读取 PostgreSQL 主键":        "failed to read PostgreSQL primary key",
	"读取 PostgreSQL 字段列表":      "failed to read PostgreSQL column list",
	"读取 PostgreSQL 索引列表":      "failed to read PostgreSQL index list",
	"读取当前 MySQL 数据库":          "failed to read current MySQL database",
	"读取当前 PostgreSQL schema":  "failed to read current PostgreSQL schema",
	"SQL 执行已取消":               "SQL execution was canceled",
	"Redis SCAN 失败":           "Redis SCAN failed",
	"Redis TYPE 失败":           "Redis TYPE failed",
	"Redis 连接不支持此操作":          "this operation is not supported for Redis connections",
	"该连接不是 MySQL":             "the connection is not MySQL",
	"该连接不是 PostgreSQL":        "the connection is not PostgreSQL",
	"该连接不是 Redis":             "the connection is not Redis",
	"表名不能为空":                  "table name cannot be empty",
	"TLS CA 文件没有有效证书":         "TLS CA file contains no valid certificates",

	"内容疑似包含密钥（如 password/token/api_key 赋值），已拒绝保存；请去掉密钥后再保存": "content appears to contain secrets such as password/token/api_key assignments; save was rejected, remove the secrets and try again",
	"（该主题下没有记忆）": "(no memories under this topic)",
	"（记忆库为空）":    "(memory store is empty)",
	"ids 不能为空":   "ids cannot be empty",

	"命中自定义危险规则":                      "matched a custom danger rule",
	"SQL 引号或注释未闭合，无法安全分析":            "SQL quotes or comments are unclosed; cannot analyze safely",
	"空 SQL 或无有效语句":                   "empty SQL or no valid statement",
	"EXPLAIN ANALYZE 目标不明确":          "EXPLAIN ANALYZE target is unclear",
	"EXPLAIN 非只读语句需要确认":              "non-read-only EXPLAIN statements require confirmation",
	"禁止 DROP DATABASE / DROP SCHEMA": "DROP DATABASE / DROP SCHEMA is forbidden",
	"DROP 会删除数据库对象":                  "DROP deletes database objects",
	"TRUNCATE 会清空数据表":                "TRUNCATE empties database tables",
	"DELETE 会删除数据":                   "DELETE deletes data",
	"COPY PROGRAM 执行系统命令":            "COPY PROGRAM executes system commands",
	"COPY 会读写数据库或文件":                 "COPY reads or writes databases or files",
	"SQL 写入、会话或事务控制":                 "SQL writes, session changes, or transaction control",
	"未知 SQL 语句需要确认":                  "unknown SQL statements require confirmation",
	"未知 SQL 函数可能有写入或锁副作用":            "unknown SQL functions may have write or locking side effects",
	"SQL 会话状态变更":                     "SQL session state change",
	"只读前缀中包含写操作":                     "read-only prefix contains a write operation",
	"SELECT 写入关键系统路径":                "SELECT writes to a critical system path",
	"SELECT 会写入服务器文件":                "SELECT writes server files",
	"SELECT INTO 会创建或修改表":            "SELECT INTO creates or modifies tables",
	"锁行查询需要确认":                       "row-locking queries require confirmation",
	"锁表或锁行查询需要确认":                    "table- or row-locking queries require confirmation",
	"工具参数命中自定义危险规则":                  "tool arguments matched a custom danger rule",
	"工具参数不是有效 JSON 对象":               "tool arguments are not a valid JSON object",
	"文件路径命中自定义危险规则":                  "file path matched a custom danger rule",
	"写入或编辑文件":                        "write or edit files",
	"命令列表无效":                         "invalid command list",
	"新建终端会启动新的 shell 进程":             "creating a terminal starts a new shell process",
	"容器内执行命令":                        "execute commands inside a container",
	"容器参数命中自定义危险规则":                  "container arguments matched a custom danger rule",
	"删除容器需要始终确认":                     "deleting containers always requires confirmation",
	"Docker 容器状态变更":                  "Docker container state change",
	"创建定时提醒，到时间后将在本会话投递消息":           "create a scheduled reminder that delivers a message to this conversation when due",
	"未知工具":                           "unknown tool",
	"无法验证历史或补全后的终端输入":                "cannot verify terminal input produced by history or completion",
	"终端控制键可能中断或改变程序":                 "terminal control keys may interrupt or alter programs",
	"回车会提交终端中已有的命令":                  "Enter submits the command already present in the terminal",
	"sort 包含无法识别的长选项":                "sort contains an unrecognized long option",
	"sort 输出到文件":                     "sort writes output to a file",
	"sort 调用外部程序，内容无法验证":             "sort invokes an external program whose contents cannot be verified",
	"xxd 反向转换会写入输出文件":                "xxd reverse conversion writes an output file",
	"xxd 指定了输出文件":                    "xxd specifies an output file",
	"主机名变更":                          "hostname change",
	"主机名或系统信息变更":                     "hostname or system information change",
	"系统时间变更":                         "system time change",
	"日志清理或轮转":                        "log cleanup or rotation",
	"内核日志清理":                         "kernel log cleanup",
	"网络配置或网络命名空间执行":                  "network configuration or execution in a network namespace",
	"网络接口配置变更":                       "network interface configuration change",
	"路由配置变更":                         "routing configuration change",
	"ARP 配置变更":                       "ARP configuration change",
	"管道把内容交给解释器执行，NexTerm 无法审查这些内容":  "the pipe hands content to an interpreter that NexTerm cannot review",
	"进程调度参数变更":                       "process scheduling parameter change",
	"xargs 从文件构建命令":                  "xargs builds commands from a file",
	"xargs 以未受控输入调用递归删除":             "xargs invokes recursive deletion with uncontrolled input",
	"xargs 从标准输入构建命令":                "xargs builds commands from standard input",
	"xargs 的分批参数无法精确建模":              "xargs batch arguments cannot be modeled precisely",
	"xargs 的分批参数冲突":                  "conflicting xargs batch arguments",
	"xargs 的分隔符参数冲突":                 "conflicting xargs delimiter arguments",
	"xargs 输入无法精确解析":                 "xargs input cannot be parsed precisely",
	"xargs -n 参数无效":                  "invalid xargs -n argument",
	"xargs -L 参数无效":                  "invalid xargs -L argument",
	"本会话路径授权":                        "session path grant",
	"只读权限模式禁止此操作":                    "read-only permission mode forbids this operation",
	"无人值守执行无法人工确认，已拒绝":               "unattended execution cannot be confirmed by a user; denied",
	"路径规则授权":                         "authorized by path rule",
	"设备长期授权":                         "persistent device grant",

	"命令嵌套过深，无法安全分析":        "command nesting is too deep for safe analysis",
	"命令超出分析预算，无法安全分析":      "command exceeds the analysis budget and cannot be analyzed safely",
	"命令结构异常，无法安全分析":        "abnormal command structure; cannot analyze safely",
	"命令结构超出分析预算":           "command structure exceeds the analysis budget",
	"函数定义会使后续调用无法审查":       "function definitions prevent later calls from being reviewed",
	"算术命令无法安全分析":           "arithmetic commands cannot be analyzed safely",
	"命令结构无法识别":             "unrecognized command structure",
	"测试表达式包含动态替换":          "test expression contains dynamic substitution",
	"循环迭代值包含动态替换":          "loop iteration values contain dynamic substitution",
	"C 风格循环无法安全分析":         "C-style loops cannot be analyzed safely",
	"case 取值包含动态替换":        "case values contain dynamic substitution",
	"case 模式包含动态替换":        "case patterns contain dynamic substitution",
	"无法识别的命令连接符":           "unrecognized command connector",
	"会把输出写入文件":             "the output is written to a file",
	"here-string 包含动态替换":   "here-string contains dynamic substitution",
	"重定向到网络设备":             "redirection to a network device",
	"重定向目标包含动态替换":          "redirection target contains dynamic substitution",
	"无法识别的重定向":             "unrecognized redirection",
	"非常规文件描述符重定向无法安全分析":    "nonstandard file-descriptor redirection cannot be analyzed safely",
	"here-document 内容缺失":   "here-document content is missing",
	"here-document 包含动态替换": "here-document contains dynamic substitution",
	"重定向目标包含通配符":           "redirection target contains a wildcard",
	"禁止写入关键系统路径":           "writing to critical system paths is forbidden",
	"禁止直接写入块设备":            "writing directly to block devices is forbidden",
	"命令里有 $(…)、变量拼出的命令名等动态内容，执行前无法确定实际会运行什么": "the command contains dynamic content such as $(…) or a variable command name, so what actually runs cannot be determined beforehand",
	"数组或下标赋值无法安全分析":                  "array or subscript assignments cannot be analyzed safely",
	"赋值包含动态替换，无法安全分析":                "assignment contains dynamic substitution and cannot be analyzed safely",
	"赋值注入可执行环境变量":                    "assignment injects executable environment variables",
	"命令名包含通配符":                       "command name contains a wildcard",
	"包含权限提升":                         "contains privilege escalation",
	"禁止以 root 编辑关键系统路径":              "editing critical system paths as root is forbidden",
	"以 root 编辑文件":                    "editing files as root",
	"包含身份切换":                         "contains identity switching",
	"su 包含无法识别的长选项":                  "su contains an unrecognized long option",
	"su 包含无法识别的选项":                   "su contains an unrecognized option",
	"su 从标准输入执行命令":                   "su executes commands from standard input",
	"su 启动身份切换会话":                    "su starts an identity-switching session",
	"env 包含无法识别的长选项":                 "env contains an unrecognized long option",
	"env 字符串拆分结果为空":                  "env string splitting produced an empty result",
	"env 包含无法识别的选项":                  "env contains an unrecognized option",
	"env 拆分并执行字符串需要确认":               "env splitting and executing strings requires confirmation",
	"文件系统写操作":                        "filesystem write operation",
	"系统关机或重启":                        "system shutdown or reboot",
	"系统运行级别变更":                       "system runlevel change",
	"进程终止":                           "process termination",
	"系统设置变更":                         "system setting change",
	"挂载或卸载文件系统":                      "mounts or unmounts a file system",
	"向 nginx 发送控制信号":                 "sends a control signal to nginx",
	"nginx 的这个参数没有对应规则":              "there is no rule for this nginx argument",
	"go 的这个子命令会编译或运行代码":              "this go subcommand builds or runs code",
	"awk 程序可以执行系统命令或写文件":             "awk programs can run system commands or write files",
	"网络工具可以收发任意数据":                   "network tools can send and receive arbitrary data",
	"sed 会就地修改文件或读取脚本文件":             "sed edits files in place or reads a script file",
	"sed 脚本会写文件或执行命令":                "the sed script writes files or runs commands",
	"sed 缺少脚本":                       "sed has no script",
	"sed 参数不完整":                      "sed arguments are incomplete",
	"权限或账户变更":                        "permission or account change",
	"账户变更":                           "account change",
	"Redis 客户端从标准输入执行命令":             "Redis client executes commands from standard input",
	"编辑器可以修改文件或执行命令":                 "the editor can modify files or run commands",
	"压缩工具可能修改文件":                     "compression tools may modify files",
	"解压会写入文件":                        "decompression writes files",
	"网络工具执行命令":                       "network tool executes commands",
	"网络工具需要确认":                       "network tools require confirmation",
	"cmake 执行构建或安装脚本，内容无法验证":         "cmake executes build or install scripts whose contents cannot be verified",
	"cmake 生成构建文件":                   "cmake generates build files",
	"别名定义会使后续命令无法审查":                 "alias definitions prevent later commands from being reviewed",
	"别名删除会改变命令解析":                    "alias removal changes command resolution",
	"管道输入进入无法识别的命令，无法安全分析":           "pipeline input reaches an unrecognized command and cannot be analyzed safely",
	"未列入只读白名单的命令":                    "command is not on the read-only allowlist",
	"会运行脚本或解释器代码":                    "runs a script or interpreter code",
	"解释器内联执行任意代码":                    "interpreter executes arbitrary inline code",
	"会打开可执行任意代码的解释器":                 "opens an interpreter that can run arbitrary code",
	"解释器代码包含危险操作":                    "interpreter code contains dangerous operations",
	"数据库客户端可能修改数据":                   "the database client may modify data",
	"数据库客户端执行重定向 SQL 文件":             "database client executes a redirected SQL file",
	"psql 执行 SQL 文件，内容无法验证":          "psql executes an SQL file whose contents cannot be verified",
	"psql 执行 shell 转义命令":             "psql executes shell escape commands",
	"mysql 执行 SQL 文件，内容无法验证":         "mysql executes an SQL file whose contents cannot be verified",
	"mysql 执行 shell 转义命令":            "mysql executes shell escape commands",
	"禁止格式化文件系统":                      "formatting filesystems is forbidden",
	"禁止递归删除系统根目录或关键目录":               "recursively deleting the system root or critical directories is forbidden",
	"禁止直接覆写块设备":                      "directly overwriting block devices is forbidden",
	"递归放开关键目录权限":                     "recursively relaxing permissions on critical directories",
	"find -delete 递归删除":              "find -delete recursively deletes files",
	"find 执行外部命令":                    "find executes external commands",
	"find 包含写入或执行动作":                 "find contains write or execution actions",
	"crontab 变更计划任务":                 "crontab changes scheduled tasks",
	"crontab 安装计划任务":                 "crontab installs scheduled tasks",
	"crontab 从标准输入安装计划任务":            "crontab installs scheduled tasks from standard input",
	"远程传输或执行需要确认":                    "remote transfer or execution requires confirmation",
	"ssh 包含无法识别的长选项":                 "ssh contains an unrecognized long option",
	"ssh 包含无法识别的选项":                  "ssh contains an unrecognized option",
	"ssh 选项执行本地或远端命令":                "ssh option executes local or remote commands",
	"ssh Include 引入配置文件，内容无法验证":      "ssh Include imports configuration files whose contents cannot be verified",
	"make --eval 内联执行代码":             "make --eval executes inline code",
	"make 包含无法识别的长选项":                "make contains an unrecognized long option",
	"make 包含无法识别的选项":                 "make contains an unrecognized option",
	"make -t 仅更新文件时间戳，不执行配方":         "make -t only updates file timestamps without executing recipes",
	"执行 Makefile 配方":                 "execute Makefile recipes",
	"make 执行 Makefile 配方，内容无法验证":     "make executes Makefile recipes whose contents cannot be verified",
	"ninja -t clean 删除构建产物":          "ninja -t clean deletes build artifacts",
	"ninja 子工具执行":                    "ninja subtool execution",
	"ninja 包含无法识别的选项":                "ninja contains an unrecognized option",
	"执行 ninja 构建规则":                  "execute ninja build rules",
	"ninja 执行 build.ninja 规则，内容无法验证": "ninja executes build.ninja rules whose contents cannot be verified",
	"scp 替换传输程序":                     "scp replaces the transfer program",
	"sftp 执行批处理文件，内容无法验证":            "sftp executes a batch file whose contents cannot be verified",
	"sftp 直接执行服务器程序":                 "sftp directly executes a server program",
	"rsync 执行传输 shell":               "rsync executes a transport shell",
	"rsync 执行远端程序":                   "rsync executes a remote program",
	"systemctl 包含无法识别的全局选项":          "systemctl contains an unrecognized global option",
	"服务或防火墙配置变更":                     "service or firewall configuration change",
	"系统服务停止或隔离":                      "system service stop or isolation",
	"软件包管理操作":                        "package management operation",
	"docker 子命令不明确":                  "docker subcommand is unclear",
	"docker 包含无法识别的全局选项":             "docker contains an unrecognized global option",
	"docker 全局信息查询":                  "docker global information query",
	"持续读取 Docker 统计信息需要确认":           "continuous reading of Docker statistics requires confirmation",
	"docker exec 包含无法识别的选项":          "docker exec contains an unrecognized option",
	"Docker 资源变更":                    "Docker resource change",
	"Docker 清理会删除资源":                 "Docker cleanup deletes resources",
	"Docker 资源操作":                    "Docker resource operation",
	"未知 Docker 子命令":                  "unknown Docker subcommand",
	"kubectl 包含无法识别的全局选项":            "kubectl contains an unrecognized global option",
	"kubectl 配置变更":                   "kubectl configuration change",
	"kubectl exec 包含无法识别的选项":         "kubectl exec contains an unrecognized option",
	"Kubernetes 资源或工作负载变更":           "Kubernetes resource or workload change",
	"未知 kubectl 子命令":                 "unknown kubectl subcommand",
	"git --exec-path 重定向子命令执行":       "git --exec-path redirects subcommand execution",
	"git 包含无法识别的全局选项":                "git contains an unrecognized global option",
	"Git 输出文件或扩展命令":                  "Git writes output files or invokes extension commands",
	"Git 分支创建、删除或改名":                 "Git branch creation, deletion, or renaming",
	"Git 远端配置变更":                     "Git remote configuration change",
	"Git 强制推送":                       "Git force push",
	"Git 推送":                         "Git push",
	"git reset --hard 丢弃提交与修改":       "git reset --hard discards commits and changes",
	"Git 仓库或远端变更":                    "Git repository or remote change",
	"未知 Git 子命令":                     "unknown Git subcommand",
	"git clean 删除未跟踪文件":              "git clean deletes untracked files",
	"git -c 注入可执行配置":                 "git -c injects executable configuration",
	"curl 配置文件可指定任意选项":               "curl configuration files can specify arbitrary options",
	"curl 保存 ETag 文件":                "curl saves ETag files",
	"下载保存或 HTTP 写入":                  "download-to-file or HTTP write",
	"curl 写入本地缓存或代码文件":               "curl writes local cache or code files",
	"curl 在服务器端执行命令":                 "curl executes commands on the server",
	"curl 将错误输出写入文件":                 "curl writes error output to a file",
	"非 GET 网络请求":                     "non-GET network request",
	"wget 默认会保存文件":                   "wget saves files by default",
	"tar 包含无法识别的长选项":                 "tar contains an unrecognized long option",
	"tar 外部程序选项缺少值":                  "tar external-program option is missing a value",
	"tar 包含外部命令执行":                   "tar contains external command execution",
	"tar 解包到关键路径":                    "tar extracts to a critical path",
	"压缩包解包或创建需要确认":                   "archive extraction or creation requires confirmation",
	"redis-cli 包含无法识别的选项":            "redis-cli contains an unrecognized option",
	"交互式 Redis 客户端需要确认":              "interactive Redis clients require confirmation",
	"Redis DEBUG 可能改变服务状态":           "Redis DEBUG may change service state",
	"Redis CLIENT 子命令需要确认":           "Redis CLIENT subcommands require confirmation",
	"Redis MEMORY 子命令需要确认":           "Redis MEMORY subcommands require confirmation",
	"Redis 数据或服务破坏性操作":               "destructive Redis data or service operation",
	"Redis 写入或未知命令":                  "Redis write or unknown command",
}

var modelAdditionalStaticText = map[string]string{
	"设备授权至少需要一个种类":                                           "device grant requires at least one kind",
	"设备授权种类仅支持 terminal_write/session_exec":                  "device grant kinds only support terminal_write/session_exec",
	"设备授权缺少设备 ID":                                            "device grant requires a device ID",
	"授权规则缺少设备 ID":                                            "grant rule requires a device ID",
	"授权规则动作仅支持 write_file/edit_file/exec_commands/send_keys": "grant rule action only supports write_file/edit_file/exec_commands/send_keys",
	"文件写入规则必须限定路径范围":                                         "file write rules must specify a path scope",
	"该动作的规则不支持路径范围":                                          "rules for this action do not support a path scope",
	"授权规则过期时间必须晚于当前时间":                                       "grant rule expiration must be later than the current time",
	"授权规则缺少规则 ID":                                            "grant rule requires a rule ID",
	"设备权限模式缺少设备 ID":                                          "device permission mode requires a device ID",
	"权限模式仅支持 read_only/read_write/silent/unattended":         "permission mode only supports read_only/read_write/silent/unattended",
	"模型返回了空 tool call ID":                                    "model returned an empty tool call ID",
	"等待结束，未观察到新命令开始（终端可能不支持 OSC 133，结果未验证）":                  "wait ended; no new command start was observed (terminal may not support OSC 133; result is unverified)",
	"等待结束，未观察到新命令开始（若仅发送文本未回车，属预期）":                          "wait ended; no new command start was observed (expected if only text without Enter was sent)",
	"发送时已有命令在运行，该命令已结束":                                      "a command was already running when keys were sent; that command has finished",
	"发送前已有命令在运行，该命令仍在运行（本次按键已送入该程序）":                         "a command was already running before keys were sent and is still running; these keys were delivered to that program",
	"timeout_ms 必须在 1-60000 之间":                              "timeout_ms must be between 1 and 60000",

	"接管任务不存在或已结束":                 "takeover task not found or already ended",
	"接管令牌已过期":                     "takeover token expired",
	"当前接管已有活动任务":                  "current takeover already has an active task",
	"本次接管禁止终端写入":                  "terminal writes are disabled for this takeover",
	"接管恢复范围与终端当前资产不一致":            "takeover recovery scope does not match the terminal's current asset",
	"接管等待用户确认":                    "takeover is waiting for user confirmation",
	"接管已被用户暂停":                    "takeover was paused by the user",
	"接管暂停超时升级":                    "takeover pause timed out and escalated",
	"tabId 或终端服务无效":               "invalid tabId or terminal service",
	"接管服务已关闭":                     "takeover service is closed",
	"新的接管":                        "new takeover",
	"接管 job ID 重复":                "duplicate takeover job ID",
	"接管 Eino ChatModel、终端或事件流未配置": "takeover Eino ChatModel, terminal, or event stream is not configured",
	"tabId 和 instruction 不能为空":    "tabId and instruction cannot be empty",
	"maxSteps 必须在 1-100 之间":       "maxSteps must be between 1 and 100",
	"接管 job ID 为空":                "takeover job ID is empty",
	"接管事件流为空":                     "takeover event stream is nil",
	"用户取消":                        "user canceled",
	"暂停超时：模型未在安全点内响应，任务已停止":       "pause timed out: model did not respond at a safe point, and the task was stopped",
	"tabId、token 和 jobId 不能为空":    "tabId, token, and jobId cannot be empty",
	"用户退出":                        "user exited",
	"用户夺回":                        "user took back control",
	"用户暂停":                        "user paused",
	"接管 checkpoint 未能持久化，暂停不可恢复":  "takeover checkpoint could not be persisted, so the pause is not recoverable",
	"任务完成":                        "task completed",
	"模型停止":                        "model stopped",
	"达到最大步骤":                      "maximum steps reached",
	"任务失败":                        "task failed",
	"收到无法识别的接管 interrupt":         "received an unrecognized takeover interrupt",
	"终端接管只支持向当前接管的终端发送按键":         "terminal takeover only supports sending keys to the currently controlled terminal",
	"该调用已执行但结果未知，未重复发送；请读取屏幕确认终端状态": "this call was already executed but its result is unknown; it was not sent again, read the screen to confirm terminal state",

	"定时任务必须关联一个会话":                         "scheduled jobs must be associated with a conversation",
	"定时任务的提示词不能为空":                         "scheduled job prompt cannot be empty",
	"超时时间不能为负数":                            "timeout cannot be negative",
	"定时任务不存在，可能已被删除，请刷新后重试":                "scheduled job not found; it may have been deleted, refresh and try again",
	"定时任务已被其他操作修改，请刷新后重试":                  "scheduled job was modified by another operation, refresh and try again",
	"当前会话的定时任务数量已达上限，请删除不再需要的任务后再创建":       "scheduled job limit for this conversation reached; delete unused jobs before creating another",
	"任务运行中，请等待结束后再操作":                      "job is running; wait for it to finish before retrying",
	"定时任务调度器已在运行":                          "scheduled job scheduler is already running",
	"定时表达式无效，请检查格式（分 时 日 月 周，如 0 9 * * *）": "invalid schedule expression; use minute hour day month weekday, for example 0 9 * * *",
	"定时任务存储失败，请重试":                         "scheduled job storage failed; try again",
	"定时任务执行超时":                             "scheduled job execution timed out",
	"定时任务租约丢失，本次运行已中断":                     "scheduled job lease was lost; this run was interrupted",

	"数据库路径不能为空": "database path cannot be empty",
	"ID 格式不正确":  "invalid ID format",
	"片段不存在":     "snippet not found",
	"凭据不存在":     "credential not found",
	"凭据墓碑不存在":   "credential tombstone not found",
	"会话记录不存在":   "transcript not found",
	"该记录已关闭同步并清除远端副本, 不能重新开启": "sync was disabled for this recording and its remote copy was deleted; it cannot be enabled again",
	"进行中的会话不能开启同步":            "sync cannot be enabled for an active session",
	"会话不存在":                   "conversation not found",
	"消息不存在":                   "message not found",
	"资产不存在":                   "asset not found",
	"资产名称不能为空":                "asset name cannot be empty",
	"\"当前设备\"是内置资产，不能删除":      "the built-in asset \"当前设备\" cannot be deleted",
	"当前设备不可用不存在":              "current device is unavailable",
	"内置资产\"当前设备\"不接受同步写入（各设备身份相互独立）": "the built-in asset \"当前设备\" does not accept sync writes because each device has an independent identity",
	"分组不存在":               "group not found",
	"分组不能将自身设为父级":         "a group cannot be its own parent",
	"不允许将分组移动到其后代分组下":     "a group cannot be moved below one of its descendants",
	"分组名称不能为空":            "group name cannot be empty",
	"开启已知主机同步需要先登录账号":     "enabling known host sync requires signing in",
	"开启 AI 模型档案同步需要先登录账号": "enabling AI model profile sync requires signing in",
	"开启 AI 权限同步需要先登录账号":   "enabling AI permission sync requires signing in",
	"开启外观与快捷键同步需要先登录账号":   "enabling appearance and shortcut sync requires signing in",
	"开启 AI 授权同步需要先登录账号":   "enabling AI grant sync requires signing in",
	"AI 运行不存在":            "AI run not found",
	"数据库清理被读事务阻塞":         "database cleanup is blocked by a read transaction",
}
