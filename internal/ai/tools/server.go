package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (r *Registry) transport(ctx context.Context, scope Scope) (base.Transport, error) {
	if r.deps.Transport == nil {
		return nil, errors.New("会话传输未配置")
	}
	if scope.SessionID == "" {
		return nil, errors.New("当前 AI 作用域没有 sessionId")
	}
	return r.deps.Transport(ctx, scope.SessionID)
}

func (r *Registry) fileSystem(ctx context.Context, scope Scope) (base.FileSystem, error) {
	transport, err := r.transport(ctx, scope)
	if err != nil {
		return nil, err
	}
	files, ok := transport.(base.FileTransport)
	if !ok {
		return nil, errors.New("当前会话不支持文件操作")
	}
	return files.FileSystem(ctx)
}

func (r *Registry) execCommands(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Commands []string `json:"commands"`
		Timeout  int      `json:"timeout"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if len(args.Commands) == 0 || len(args.Commands) > 20 {
		return Fail(invalid("commands 必须包含 1-20 条命令"))
	}
	for _, command := range args.Commands {
		if strings.TrimSpace(command) == "" {
			return Fail(invalid("命令不能为空"))
		}
	}
	if args.Timeout == 0 {
		args.Timeout = 60
	}
	if args.Timeout < 1 || args.Timeout > 300 {
		return Fail(invalid("timeout 必须在 1-300 秒之间"))
	}
	transport, err := r.transport(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	var output strings.Builder
	exitCode := 0
	truncated := false
	for _, command := range args.Commands {
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		commandContext, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
		result, execErr := transport.Exec(commandContext, command, base.ExecOptions{Timeout: time.Duration(args.Timeout) * time.Second, ExpectedGeneration: transport.Generation()})
		cancel()
		fmt.Fprintf(&output, "$ %s\n", command)
		stdout, stdoutCut := capText(result.Stdout)
		stderr, stderrCut := capText(result.Stderr)
		truncated = truncated || stdoutCut || stderrCut || result.Truncated
		output.WriteString(stdout)
		if stderr != "" {
			output.WriteString("\n[stderr]\n")
			output.WriteString(stderr)
		}
		if result.ExitCode != nil {
			exitCode = *result.ExitCode
			fmt.Fprintf(&output, "\n[exit_code=%d]\n", exitCode)
		}
		if execErr != nil {
			if ctx.Err() != nil {
				return Fail(ctx.Err())
			}
			if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
				return Fail(execErr)
			}
			exitCode = 1
			fmt.Fprintf(&output, "\n[执行失败: %v]\n", execErr)
		}
		output.WriteByte('\n')
	}
	text, cut := capText(output.String())
	return Output{OK: true, Text: text, ExitCode: exitCode, Truncated: truncated || cut}
}

func (r *Registry) readFile(ctx context.Context, jobID string, scope Scope, raw []byte) Output {
	var args struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return Fail(invalid("path 不能为空"))
	}
	if args.MaxBytes == 0 {
		args.MaxBytes = 120 << 10
	}
	if args.MaxBytes < 1 || args.MaxBytes > 2<<20 {
		return Fail(invalid("max_bytes 必须在 1 字节到 2 MiB 之间"))
	}
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	content, readErr := files.ReadFile(ctx, args.Path, args.MaxBytes)
	if readErr != nil {
		exists, existsErr := files.Exists(ctx, args.Path)
		if existsErr == nil && !exists {
			r.state(jobID).rememberRead(targetKey(scope, args.Path), fileVersion{Exists: false})
		}
		return Fail(readErr)
	}
	r.state(jobID).rememberRead(targetKey(scope, args.Path), versionOf(content))
	text, truncated := capText(strings.ToValidUTF8(string(content), "�"))
	return Output{OK: true, Text: text, Truncated: truncated}
}

// ReadRefFile 为 @ 引用注入读取文件内容，复用 read_file 的读取守卫（默认 120 KiB 上限），
// 不做版本登记；展示层截断由调用方负责。
func (r *Registry) ReadRefFile(ctx context.Context, scope Scope, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", invalid("path 不能为空")
	}
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return "", err
	}
	content, err := files.ReadFile(ctx, path, 120<<10)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(content), "�"), nil
}

func (r *Registry) listDir(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Path string `json:"path"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return Fail(invalid("path 不能为空"))
	}
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	entries, err := files.List(ctx, args.Path)
	if err != nil {
		return Fail(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	truncated := len(entries) > 500
	if truncated {
		entries = entries[:500]
	}
	var output strings.Builder
	for _, entry := range entries {
		kind := "-"
		if entry.Kind == base.FileDirectory {
			kind = "d"
		}
		fmt.Fprintf(&output, "%s %10d %s\n", kind, entry.Size, entry.Name)
	}
	return Output{OK: true, Text: output.String(), Truncated: truncated}
}

func (r *Registry) searchFiles(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		By      string `json:"by"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if strings.TrimSpace(args.Path) == "" || args.Pattern == "" || args.By != "name" && args.By != "content" {
		return Fail(invalid("path/pattern 不能为空，by 必须为 name 或 content"))
	}
	files, err := r.fileSystem(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	searchContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	search := fileSearch{files: files, pattern: args.Pattern, by: args.By, results: make([]string, 0, 100)}
	if args.By == "name" {
		if _, err := path.Match(args.Pattern, "probe"); err != nil {
			return Fail(invalid("文件名 glob 无效: %v", err))
		}
	} else {
		search.regex, err = regexp.Compile(args.Pattern)
		if err != nil {
			return Fail(invalid("内容正则无效: %v", err))
		}
	}
	if err := search.walk(searchContext, args.Path, 0); err != nil {
		return Fail(err)
	}
	text := strings.Join(search.results, "\n")
	if text == "" {
		text = "无匹配"
	}
	text, cut := capText(text)
	return Output{OK: true, Text: text, Truncated: search.truncated || cut}
}

type fileSearch struct {
	files     base.FileSystem
	pattern   string
	by        string
	regex     *regexp.Regexp
	results   []string
	visited   int
	truncated bool
}

func (s *fileSearch) walk(ctx context.Context, directory string, depth int) error {
	if len(s.results) >= 100 || depth > 6 || s.visited > 10000 {
		s.truncated = true
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.files.List(ctx, directory)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.visited++
		if s.visited > 10000 {
			s.truncated = true
			return nil
		}
		entryPath := entry.Path
		if entryPath == "" {
			entryPath = strings.TrimRight(directory, "/") + "/" + entry.Name
		}
		if entry.Kind == base.FileDirectory {
			if err := s.walk(ctx, entryPath, depth+1); err != nil {
				return err
			}
			continue
		}
		if entry.Kind != base.FileFile {
			continue
		}
		if s.by == "name" {
			matched, _ := path.Match(s.pattern, entry.Name)
			if matched {
				s.results = append(s.results, entryPath)
			}
		} else if entry.Size <= 1<<20 {
			if err := s.searchContent(ctx, entryPath); err != nil {
				return err
			}
		}
		if len(s.results) >= 100 {
			s.results = s.results[:100]
			s.truncated = true
			return nil
		}
	}
	return nil
}

func (s *fileSearch) searchContent(ctx context.Context, name string) error {
	content, err := s.files.ReadFile(ctx, name, 1<<20)
	if err != nil || !utf8.Valid(content) {
		return nil
	}
	matches := 0
	for lineNumber, line := range strings.Split(string(content), "\n") {
		if s.regex.MatchString(line) {
			s.results = append(s.results, fmt.Sprintf("%s:%d:%s", name, lineNumber+1, line))
			matches++
			if matches == 3 || len(s.results) >= 100 {
				break
			}
		}
	}
	return nil
}

func (r *Registry) resolveTab(scope Scope, requested string) (string, error) {
	if scope.TabID == "" {
		return "", errors.New("当前 AI 作用域没有 tabId")
	}
	if requested != "" && requested != scope.TabID {
		return "", ErrCrossScope
	}
	if r.deps.TabSession != nil && scope.SessionID != "" && r.deps.TabSession(scope.TabID) != scope.SessionID {
		return "", ErrCrossScope
	}
	if r.deps.Terminal == nil {
		return "", errors.New("终端服务未配置")
	}
	return scope.TabID, nil
}

// commandState reads the tab's OSC 133 command lifecycle snapshot when the
// terminal tracks it, and the zero state otherwise.
func (r *Registry) commandState(ctx context.Context, tabID string) (CommandState, error) {
	if terminal, ok := r.deps.Terminal.(CommandStateTerminal); ok {
		return terminal.CommandState(ctx, tabID)
	}
	return CommandState{}, nil
}

func (r *Registry) readScreen(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		TabID    string `json:"tab_id"`
		SinceSeq uint64 `json:"since_seq"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if args.MaxBytes == 0 {
		args.MaxBytes = defaultDiffBytes
	}
	if args.MaxBytes < 1 || args.MaxBytes > MaxOutputBytes {
		return Fail(invalid("max_bytes 必须在 1-%d 字节之间", MaxOutputBytes))
	}
	tabID, err := r.resolveTab(scope, args.TabID)
	if err != nil {
		return Fail(err)
	}
	if args.SinceSeq > 0 {
		return r.readScreenSince(ctx, tabID, args.SinceSeq, int(args.MaxBytes))
	}
	screen, err := r.deps.Terminal.Snapshot(ctx, tabID)
	if err != nil {
		return Fail(err)
	}
	header := fmt.Sprintf("光标 (%d, %d)，空闲 %dms，屏幕序号 %d", screen.CursorRow, screen.CursorCol, screen.IdleMS, screen.Seq)
	if screen.AltScreen {
		header += "，AltScreen"
	}
	if state, stateErr := r.commandState(ctx, tabID); stateErr == nil && state.Sequence > 0 {
		status := "空闲"
		if state.Running {
			status = "运行中"
		}
		line := fmt.Sprintf("命令状态：%s，序号 %d", status, state.Sequence)
		if state.HasLastExitCode {
			line += fmt.Sprintf("，上次退出码 %d", state.LastExitCode)
		}
		header += "\n" + line
	}
	return OK(header + "\n" + screen.Text)
}

// readScreenSince returns only the output produced after the anchor sequence,
// bounded by maxBytes. The header carries the exact continuation anchor
// (start+len(data)) so the next page neither repeats nor skips output, plus
// explicit expired-anchor and render-truncation recovery notes.
func (r *Registry) readScreenSince(ctx context.Context, tabID string, sinceSeq uint64, maxBytes int) Output {
	data, start, latest, err := r.outputSince(ctx, tabID, sinceSeq, maxBytes)
	if err != nil {
		return Fail(err)
	}
	cols, rows := 80, 24
	if screen, snapshotErr := r.deps.Terminal.Snapshot(ctx, tabID); snapshotErr == nil {
		cols, rows = screen.Cols, screen.Rows
	}
	lines := renderOutputDiff(data, cols, rows)
	next := start + uint64(len(data))
	header := fmt.Sprintf("新输出（序号 %d → %d，最新 %d）", start, next, latest)
	if start > sinceSeq {
		header += "；锚点已过期，从最早保留输出开始"
	}
	more := next < latest
	if more {
		header += fmt.Sprintf("；还有未读输出，下一页 since_seq=%d", next)
	}
	text, truncated := capText(strings.Join(lines, "\n"))
	if truncated {
		header += fmt.Sprintf("；本页渲染已截断，用 since_seq=%d 配合更小 max_bytes 重读", start)
	}
	return Output{OK: true, Text: header + "\n" + text, Truncated: truncated || more}
}

func (r *Registry) sendKeys(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Keys   string `json:"keys"`
		Enter  bool   `json:"enter"`
		WaitMS int    `json:"wait_ms"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if args.WaitMS < 0 || args.WaitMS > maxSendKeysWaitMS {
		return Fail(invalid("wait_ms 必须在 0-%d 毫秒之间", maxSendKeysWaitMS))
	}
	tabID, err := r.resolveTab(scope, "")
	if err != nil {
		return Fail(err)
	}
	encoded, err := EncodeKeys(args.Keys, args.Enter)
	if err != nil {
		return Fail(err)
	}
	before, err := r.commandState(ctx, tabID)
	if err != nil {
		return Fail(err)
	}
	if err := r.deps.Terminal.Write(ctx, tabID, encoded); err != nil {
		return Fail(err)
	}
	if r.deps.Audit != nil {
		_ = r.deps.Audit(context.WithoutCancel(ctx), AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: "takeover", Payload: map[string]any{"keys": "<redacted>", "enter": args.Enter, "tab": tabID}})
	}
	if args.WaitMS == 0 {
		return OK("已发送" + screenAnchorSuffix(ctx, r.deps.Terminal, tabID))
	}
	if _, ok := r.deps.Terminal.(CommandStateTerminal); !ok {
		return OK("已发送" + screenAnchorSuffix(ctx, r.deps.Terminal, tabID) + "；该终端不支持 OSC 133 命令跟踪，无法确认命令是否完成，请用 wait_for 校验新输出")
	}
	return r.waitCommandFinish(ctx, tabID, before, args.WaitMS)
}

// screenAnchorSuffix reports the post-write screen sequence so the caller can
// anchor a wait_for since_seq read; it degrades to empty when unavailable.
func screenAnchorSuffix(ctx context.Context, terminal Terminal, tabID string) string {
	screen, err := terminal.Snapshot(ctx, tabID)
	if err != nil || screen.Seq == 0 {
		return ""
	}
	return fmt.Sprintf("（屏幕序号 %d）", screen.Seq)
}

const (
	maxSendKeysWaitMS = 10_000
	busyReturnGraceMS = 1_500
	commandTailLines  = 10
)

// waitCommandFinish polls the tab command state after a send_keys write until
// the newly started command finishes, the wait budget expires, or the
// pre-existing busy state resolves. A pre-write Sequence of 0 is not evidence
// of missing OSC 133 support (a fresh shell has not run its first command
// yet), so the wait budget is always honored and only the outcome is
// reported. The exit code shown is always bound to the command this wait
// observed finish, never to an earlier one.
func (r *Registry) waitCommandFinish(ctx context.Context, tabID string, before CommandState, waitMS int) Output {
	waitContext, cancel := context.WithTimeout(ctx, time.Duration(waitMS)*time.Millisecond)
	defer cancel()
	if before.Running {
		return r.waitBusyClear(ctx, waitContext, tabID, before.Sequence)
	}
	for {
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		state, err := r.commandState(waitContext, tabID)
		if err != nil {
			return Fail(err)
		}
		if state.Sequence > before.Sequence && !state.Running {
			return r.commandFinishOutput(ctx, tabID, state, before.Sequence+1, "命令已完成")
		}
		if waitContext.Err() != nil {
			if state.Running || state.Sequence > before.Sequence {
				return r.commandFinishOutput(ctx, tabID, state, before.Sequence+1, "等待结束，命令仍在运行")
			}
			headline := "等待结束，未观察到新命令开始"
			if before.Sequence == 0 {
				headline += "（终端可能不支持 OSC 133，结果未验证）"
			} else {
				headline += "（若仅发送文本未回车，属预期）"
			}
			return r.commandFinishOutput(ctx, tabID, state, before.Sequence+1, headline)
		}
		select {
		case <-waitContext.Done():
		case <-time.After(r.deps.pollInterval()):
		}
	}
}

// waitBusyClear handles a write that landed while a command was already
// running: it returns as soon as that command finishes, or after a short
// grace period with an explicit busy note instead of burning the full wait.
// The busy command's own sequence is the minimum for exit-code attribution.
func (r *Registry) waitBusyClear(ctx, waitContext context.Context, tabID string, busySeq uint64) Output {
	grace, cancel := context.WithTimeout(waitContext, busyReturnGraceMS*time.Millisecond)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		state, err := r.commandState(grace, tabID)
		if err != nil {
			return Fail(err)
		}
		if !state.Running {
			return r.commandFinishOutput(ctx, tabID, state, busySeq, "发送时已有命令在运行，该命令已结束")
		}
		if grace.Err() != nil {
			return r.commandFinishOutput(ctx, tabID, state, busySeq, "发送时已有命令在运行，仍在运行（本次按键已送入该程序）")
		}
		select {
		case <-grace.Done():
		case <-time.After(r.deps.pollInterval()):
		}
	}
}

// commandFinishOutput renders the wait outcome. The exit code is reported
// only when the state's code is bound to a command at or after minExitSeq
// (the tracker stamps the sequence of the command whose 133;D carried the
// code); otherwise the result is explicitly exit-unknown and must not be
// recorded or replayed as exit 0.
func (r *Registry) commandFinishOutput(ctx context.Context, tabID string, state CommandState, minExitSeq uint64, headline string) Output {
	screen, err := r.deps.Terminal.Snapshot(ctx, tabID)
	if err != nil {
		return Fail(err)
	}
	text := headline
	output := Output{OK: true, Text: text}
	if state.HasLastExitCode && state.ExitCodeSequence >= minExitSeq {
		text += fmt.Sprintf("，退出码 %d", state.LastExitCode)
		output.ExitCode = state.LastExitCode
	} else {
		output.ExitUnknown = true
	}
	text += fmt.Sprintf("\n命令序号 %d，屏幕序号 %d\n屏幕尾部：\n%s", state.Sequence, screen.Seq, tailText(screen, commandTailLines))
	output.Text = text
	return output
}

func tailText(screen Screen, maxLines int) string {
	lines := screen.Tail
	if len(lines) == 0 {
		lines = strings.Split(screen.Text, "\n")
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func (r *Registry) waitFor(ctx context.Context, scope Scope, raw []byte, maximumSeconds int) Output {
	var args struct {
		Pattern   string `json:"pattern"`
		TimeoutMS int64  `json:"timeout_ms"`
		SinceSeq  uint64 `json:"since_seq"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if args.TimeoutMS == 0 {
		args.TimeoutMS = 10_000
	}
	if args.TimeoutMS < 1 || args.TimeoutMS > int64(maximumSeconds)*1000 {
		return Fail(invalid("timeout_ms 超出 1-%d 毫秒范围", maximumSeconds*1000))
	}
	regex, err := regexp.Compile(args.Pattern)
	if err != nil {
		return Fail(invalid("正则无效: %v", err))
	}
	tabID, err := r.resolveTab(scope, "")
	if err != nil {
		return Fail(err)
	}
	waitContext, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
	defer cancel()
	if args.SinceSeq > 0 {
		return r.waitForNewOutput(waitContext, tabID, regex, args.SinceSeq)
	}
	ticker := time.NewTicker(r.deps.pollInterval())
	defer ticker.Stop()
	for {
		screen, snapshotErr := r.deps.Terminal.Snapshot(waitContext, tabID)
		if snapshotErr != nil {
			return Fail(snapshotErr)
		}
		tail := screen.Tail
		if len(tail) == 0 {
			tail = strings.Split(screen.Text, "\n")
		}
		if len(tail) > 30 {
			tail = tail[len(tail)-30:]
		}
		if regex.MatchString(strings.Join(tail, "\n")) {
			return OK("模式已出现")
		}
		select {
		case <-waitContext.Done():
			if ctx.Err() != nil {
				return Fail(ctx.Err())
			}
			if len(tail) > 10 {
				tail = tail[len(tail)-10:]
			}
			return Output{Text: "等待超时，最近输出：\n" + strings.Join(tail, "\n"), ExitCode: 1}
		case <-ticker.C:
		}
	}
}

// waitForNewOutput matches only output produced after the anchor sequence, so
// content that was already on screen can never false-match. The scan cursor
// advances through the new output in bounded windows, carrying an overlap so
// patterns spanning a window seam still match; every byte after the anchor is
// scanned at least once (evicted bytes are flagged in the timeout output).
func (r *Registry) waitForNewOutput(waitContext context.Context, tabID string, regex *regexp.Regexp, sinceSeq uint64) Output {
	cols, rows := 80, 24
	if screen, err := r.deps.Terminal.Snapshot(waitContext, tabID); err == nil {
		cols, rows = screen.Cols, screen.Rows
	}
	ticker := time.NewTicker(r.deps.pollInterval())
	defer ticker.Stop()
	scanPos := sinceSeq
	var carry []byte
	var recent []string
	gap := false
	for {
		data, start, latest, err := r.outputSince(waitContext, tabID, scanPos, waitScanBytes)
		if err != nil {
			return Fail(err)
		}
		if start > scanPos {
			gap = true
		}
		window := append(carry, data...)
		rendered := renderOutputDiff(window, cols, rows)
		if regex.MatchString(strings.Join(rendered, "\n")) {
			return OK(fmt.Sprintf("模式已出现（新输出，屏幕序号 %d）", latest))
		}
		if len(data) > 0 {
			recent = rendered
			keep := waitScanOverlap
			if keep > len(window) {
				keep = len(window)
			}
			carry = append(carry[:0], window[len(window)-keep:]...)
			scanPos = start + uint64(len(data))
		}
		select {
		case <-waitContext.Done():
			if waitContext.Err() == context.DeadlineExceeded {
				shown := recent
				if len(shown) > 10 {
					shown = shown[len(shown)-10:]
				}
				text := fmt.Sprintf("等待超时，锚点 %d 之后的新输出", sinceSeq)
				if gap {
					text += "（部分输出已被淘汰）"
				}
				return Output{Text: text + "：\n" + strings.Join(shown, "\n"), ExitCode: 1}
			}
			return Fail(waitContext.Err())
		case <-ticker.C:
		}
	}
}
