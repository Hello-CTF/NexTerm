package tools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
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

func (r *Registry) execCommands(ctx context.Context, jobID string, scope Scope, call Call) Output {
	var args ExecCommandsArgs
	if err := decode(call.Args, &args); err != nil {
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
	if len(args.AssetIDs) > 0 || len(args.Tags) > 0 {
		return r.execCommandsBatch(ctx, jobID, call, args)
	}
	return r.execCommandsCurrent(ctx, scope, args)
}

func (r *Registry) execCommandsCurrent(ctx context.Context, scope Scope, args ExecCommandsArgs) Output {
	transport, err := r.transport(ctx, scope)
	if err != nil {
		return Fail(err)
	}
	capOutput := func(text string) (string, string, bool) {
		display, cut := capText(text)
		model := display
		if cut {
			model = strings.TrimSuffix(display, "\n[输出已截断]") + "\n[output truncated]"
		}
		return display, model, cut
	}
	var output, modelOutput strings.Builder
	exitCode := 0
	truncated := false
	execFailed := false
	for _, command := range args.Commands {
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		commandContext, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
		result, execErr := transport.Exec(commandContext, command, base.ExecOptions{Timeout: time.Duration(args.Timeout) * time.Second, ExpectedGeneration: transport.Generation()})
		cancel()
		fmt.Fprintf(&output, "$ %s\n", command)
		fmt.Fprintf(&modelOutput, "$ %s\n", command)
		stdout, modelStdout, stdoutCut := capOutput(result.Stdout)
		stderr, modelStderr, stderrCut := capOutput(result.Stderr)
		truncated = truncated || stdoutCut || stderrCut || result.Truncated
		output.WriteString(stdout)
		modelOutput.WriteString(modelStdout)
		if stderr != "" {
			output.WriteString("\n[stderr]\n")
			output.WriteString(stderr)
			modelOutput.WriteString("\n[stderr]\n")
			modelOutput.WriteString(modelStderr)
		}
		if result.ExitCode != nil {
			exitCode = *result.ExitCode
			fmt.Fprintf(&output, "\n[exit_code=%d]\n", exitCode)
			fmt.Fprintf(&modelOutput, "\n[exit_code=%d]\n", exitCode)
		}
		if execErr != nil {
			if ctx.Err() != nil {
				return Fail(ctx.Err())
			}
			if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
				return Fail(execErr)
			}
			exitCode = 1
			execFailed = true
			fmt.Fprintf(&output, "\n[执行失败: %v]\n", execErr)
			fmt.Fprintf(&modelOutput, "\n[execution failed: %v]\n", execErr)
		}
		output.WriteByte('\n')
		modelOutput.WriteByte('\n')
	}
	text, _, cut := capOutput(output.String())
	_, modelText, _ := capOutput(modelOutput.String())
	return Output{OK: true, Text: text, ModelText: modelText, ExitCode: exitCode, Truncated: truncated || cut, ExecFailed: execFailed}
}

func normalizeExecTargetFilters(assetIDs, tags []string) ([]string, []string, error) {
	if len(assetIDs) > execbatch.MaxTargets || len(tags) > 32 {
		return nil, nil, invalid("asset_ids 最多 %d 个，tags 最多 32 个", execbatch.MaxTargets)
	}
	normalize := func(values []string) ([]string, error) {
		result := make([]string, 0, len(values))
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				return nil, invalid("asset_ids/tags 不能包含空值")
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
		return result, nil
	}
	assets, err := normalize(assetIDs)
	if err != nil {
		return nil, nil, err
	}
	labels, err := normalize(tags)
	if err != nil {
		return nil, nil, err
	}
	return assets, labels, nil
}

func (r *Registry) execCommandsBatch(ctx context.Context, jobID string, call Call, args ExecCommandsArgs) Output {
	if r.deps.ResolveExecTargets == nil || r.deps.ExecBatch == nil {
		return Fail(errors.New("多主机执行未配置"))
	}
	assetIDs, tags, err := normalizeExecTargetFilters(args.AssetIDs, args.Tags)
	if err != nil {
		return Fail(err)
	}
	targets, err := r.deps.ResolveExecTargets(ctx, assetIDs, tags)
	if err != nil {
		return Fail(err)
	}
	if len(targets.SessionIDs) == 0 || len(targets.SessionIDs) > execbatch.MaxTargets {
		return Fail(invalid("多主机目标必须包含 1-%d 个已连接 SSH 会话", execbatch.MaxTargets))
	}
	seen := make(map[string]struct{}, len(targets.SessionIDs))
	for _, sessionID := range targets.SessionIDs {
		if strings.TrimSpace(sessionID) == "" {
			return Fail(invalid("多主机目标包含空 session ID"))
		}
		if _, exists := seen[sessionID]; exists {
			return Fail(invalid("多主机目标包含重复 session ID %s", sessionID))
		}
		seen[sessionID] = struct{}{}
	}
	if len(call.ProductionConfirmations) != 0 && len(call.ProductionConfirmations) != len(args.Commands) {
		return Fail(invalid("生产确认数量与命令数量不一致"))
	}
	if targets.Production && len(call.ProductionConfirmations) == 0 {
		return r.prepareExecBatchProduction(ctx, jobID, call, args, targets)
	}

	var output strings.Builder
	exitCode := 0
	truncated := false
	execFailed := false
	for index, command := range args.Commands {
		if err := ctx.Err(); err != nil {
			return Fail(err)
		}
		request := execBatchRequest(jobID, call, args, targets, command, index)
		if len(call.ProductionConfirmations) > 0 {
			request.ProductionConfirmation = &call.ProductionConfirmations[index]
		}
		batch, execErr := r.deps.ExecBatch(ctx, request)
		if execErr != nil {
			if ctx.Err() != nil {
				return Fail(ctx.Err())
			}
			fmt.Fprintf(&output, "\n第 %d 条命令未执行: %v\n", index+1, execErr)
			if index > 0 {
				output.WriteString("此前命令已经执行；未从头重试，请检查各主机状态后再决定下一步\n")
			}
			text, cut := capText(output.String())
			return Output{Text: text, ModelText: text, ExitCode: 1, Truncated: truncated || cut, ExecFailed: true}
		}
		fmt.Fprintf(&output, "$ %s\n", command)
		for _, result := range batch.Results {
			fmt.Fprintf(&output, "[asset_id=%s session_id=%s status=%s]\n", result.AssetID, result.SessionID, result.Status)
			stdout, stdoutCut := capText(result.Stdout)
			stderr, stderrCut := capText(result.Stderr)
			truncated = truncated || stdoutCut || stderrCut || result.Truncated
			output.WriteString(stdout)
			if stderr != "" {
				output.WriteString("\n[stderr]\n")
				output.WriteString(stderr)
			}
			if result.ExitCode != nil {
				fmt.Fprintf(&output, "\n[exit_code=%d]\n", *result.ExitCode)
				if *result.ExitCode != 0 {
					exitCode = *result.ExitCode
				}
			}
			if result.Error != "" {
				fmt.Fprintf(&output, "\n[执行失败: %s]\n", result.Error)
			}
			if result.Status != execbatch.StatusSuccess {
				if exitCode == 0 {
					exitCode = 1
				}
				if result.Status != execbatch.StatusExitError {
					execFailed = true
				}
			}
			output.WriteByte('\n')
		}
	}
	text, cut := capText(output.String())
	return Output{OK: true, Text: text, ModelText: text, ExitCode: exitCode, Truncated: truncated || cut, ExecFailed: execFailed}
}

func (r *Registry) prepareExecBatchProduction(ctx context.Context, jobID string, call Call, args ExecCommandsArgs, targets ExecTargetSet) Output {
	challenge := &ExecBatchProductionChallenge{Confirmations: make([]execbatch.ProductionConfirmation, len(args.Commands))}
	for index, command := range args.Commands {
		request := execBatchRequest(jobID, call, args, targets, command, index)
		_, err := r.deps.ExecBatch(ctx, request)
		var required *execbatch.ConfirmationRequiredError
		if !errors.As(err, &required) {
			if err != nil {
				return Fail(fmt.Errorf("生产确认预检失败: %w", err))
			}
			return Fail(errors.New("生产目标在确认预检期间发生变化，执行状态不确定；请检查各主机状态，未自动重试"))
		}
		challenge.Confirmations[index] = execbatch.ProductionConfirmation{
			Confirmed: true, TargetSetHash: required.TargetSetHash, CommandHash: required.CommandHash,
		}
		if index == 0 {
			challenge.Detail = required.IPCDetail()
		}
	}
	return Output{
		Text:     fmt.Sprintf("生产环境多主机执行需要确认（%d 个目标，%d 条命令）；尚未执行任何命令", len(targets.SessionIDs), len(args.Commands)),
		ExitCode: 1, ProductionChallenge: challenge,
	}
}

func execBatchRequest(jobID string, call Call, args ExecCommandsArgs, targets ExecTargetSet, command string, index int) execbatch.Request {
	callID := call.OriginID
	if callID == "" {
		callID = call.ID
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", jobID, callID, index)))
	return execbatch.Request{
		BatchID: fmt.Sprintf("ai-%x", sum[:16]), SessionIDs: targets.SessionIDs, Command: command,
		TimeoutMS: int64(args.Timeout) * 1000,
	}
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

// ReadRefFile reads file content for @-reference injection, reusing read_file's read guard
// (default 120 KiB limit); no version registration. Display-layer truncation is the caller's job.
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
	modelText := ""
	if text == "" {
		text = "无匹配"
		modelText = "no matches"
	}
	text, cut := capText(text)
	return Output{OK: true, Text: text, ModelText: modelText, Truncated: search.truncated || cut}
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

func (s *sessionTerminal) TabSession(ctx context.Context, tabID string) (string, error) {
	tab, err := s.manager.TabFor(sessionRequestContext(ctx), tabID)
	if err != nil {
		return "", err
	}
	return tab.SessionID, nil
}

// hasTabSessionResolver reports whether any tab-session identity resolver is
// available: the request-scoped TabSessionTerminal capability or the legacy
// Dependencies.TabSession hook. Cursor binding treats both as resolvers.
func (r *Registry) hasTabSessionResolver() bool {
	if _, ok := r.deps.Terminal.(TabSessionTerminal); ok {
		return true
	}
	return r.deps.TabSession != nil
}

func (r *Registry) tabSession(ctx context.Context, tabID string) (string, error) {
	if terminal, ok := r.deps.Terminal.(TabSessionTerminal); ok {
		return terminal.TabSession(ctx, tabID)
	}
	if r.deps.TabSession == nil {
		return "", nil
	}
	if _, authenticated := ipc.UserIDFromContext(ctx); authenticated && r.deps.Terminal != nil {
		if _, err := r.deps.Terminal.Snapshot(ctx, tabID); err != nil {
			return "", err
		}
	}
	return r.deps.TabSession(tabID), nil
}

func (r *Registry) resolveTab(ctx context.Context, jobID string, scope Scope, requested string) (string, error) {
	_, requestScoped := r.deps.Terminal.(TabSessionTerminal)
	hasResolver := requestScoped || r.deps.TabSession != nil
	if requested != "" && requested != scope.TabID {
		if createdSessionID, created := r.state(jobID).createdTab(requested); created {
			if hasResolver {
				sessionID, err := r.tabSession(ctx, requested)
				if err != nil {
					return "", err
				}
				if sessionID != createdSessionID {
					return "", ErrCrossScope
				}
			}
			if r.deps.Terminal == nil {
				return "", errors.New("终端服务未配置")
			}
			return requested, nil
		}
		// Handles are the stable reference form emitted by terminal_list and
		// terminal_create. Resolving one verifies the tab still exists and
		// still belongs to the issuing session (gone/replaced are distinct
		// failures); it never widens scope beyond the current session or
		// tabs this job created.
		if strings.HasPrefix(requested, "t/") {
			handle, err := ParseTerminalHandle(requested)
			if err != nil {
				return "", err
			}
			tabID, err := r.resolveHandle(ctx, handle)
			if err != nil {
				return "", err
			}
			if handle.SessionID != scope.SessionID {
				if _, created := r.state(jobID).createdTab(tabID); !created {
					return "", ErrCrossScope
				}
			}
			if r.deps.Terminal == nil {
				return "", errors.New("终端服务未配置")
			}
			return tabID, nil
		}
		return "", ErrCrossScope
	}
	if scope.TabID == "" {
		return "", errors.New("当前 AI 作用域没有 tabId")
	}
	if hasResolver && scope.SessionID != "" {
		sessionID, err := r.tabSession(ctx, scope.TabID)
		if err != nil {
			if errors.Is(err, session.ErrTabNotFound) {
				return "", ErrTerminalGone
			}
			return "", err
		}
		if sessionID != scope.SessionID {
			return "", ErrCrossScope
		}
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

func (r *Registry) readScreen(ctx context.Context, jobID string, scope Scope, raw []byte) Output {
	var args struct {
		TabID        string `json:"tab_id"`
		SinceSeq     uint64 `json:"since_seq"`
		MaxBytes     int64  `json:"max_bytes"`
		Cursor       string `json:"cursor"`
		HistoryLines int    `json:"history_lines"`
		Unwrap       bool   `json:"unwrap"`
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
	if args.HistoryLines < 0 || args.HistoryLines > diffHistoryLines {
		return Fail(invalid("history_lines 必须在 0-%d 行之间", diffHistoryLines))
	}
	if args.Cursor != "" && args.SinceSeq > 0 {
		return Fail(invalid("cursor 与 since_seq 不能同时使用"))
	}
	tabID, err := r.resolveTab(ctx, jobID, scope, args.TabID)
	if err != nil {
		return Fail(err)
	}
	if args.HistoryLines > 0 {
		return r.readScreenHistory(ctx, tabID, args.HistoryLines, args.Unwrap)
	}
	if args.Cursor != "" {
		cursor, err := decodeScreenCursor(args.Cursor)
		if err != nil {
			return Fail(err)
		}
		if cursor.TabID != tabID {
			return Fail(invalid("游标属于终端 %s，不能用于终端 %s", cursor.TabID, tabID))
		}
		hasResolver := r.hasTabSessionResolver()
		if cursor.SessionID == "" && hasResolver {
			return Fail(fmt.Errorf("游标缺少会话绑定，无法确认来源终端；请用 since_seq=%d 重新读取", cursor.Seq))
		}
		if cursor.SessionID != "" {
			sessionID, sessionErr := r.tabSession(ctx, tabID)
			if sessionErr != nil {
				if errors.Is(sessionErr, session.ErrTabNotFound) {
					return Fail(ErrTerminalGone)
				}
				return Fail(sessionErr)
			}
			if sessionID != cursor.SessionID {
				return Fail(fmt.Errorf("游标对应的终端会话身份已变化；请用 since_seq=%d 重新读取（原始流按当前尺寸完整重放）", cursor.Seq))
			}
		}
		// The cursor read must confirm the current geometry: resizing changes
		// how the raw stream renders into lines, and an unconfirmed geometry
		// must fail closed with the recovery anchor, never render at defaults.
		// The one validated geometry is then reused for the whole read.
		screen, snapshotErr := r.deps.Terminal.Snapshot(ctx, tabID)
		if snapshotErr != nil {
			return Fail(fmt.Errorf("无法确认终端几何（快照失败: %v）；请用 since_seq=%d 重新读取（原始流按当前尺寸完整重放）", snapshotErr, cursor.Seq))
		}
		if screen.Cols != cursor.Cols || screen.Rows != cursor.Rows {
			return Fail(fmt.Errorf("终端已从 %dx%d resize 为 %dx%d，游标失效；请用 since_seq=%d 重新读取（原始流按新尺寸完整重放）",
				cursor.Cols, cursor.Rows, screen.Cols, screen.Rows, cursor.Seq))
		}
		return r.readScreenSince(ctx, tabID, cursor.Seq, int(args.MaxBytes), args.Unwrap, screen.Cols, screen.Rows)
	}
	if args.SinceSeq > 0 {
		cols, rows := 80, 24
		if screen, snapshotErr := r.deps.Terminal.Snapshot(ctx, tabID); snapshotErr == nil {
			cols, rows = screen.Cols, screen.Rows
		}
		return r.readScreenSince(ctx, tabID, args.SinceSeq, int(args.MaxBytes), args.Unwrap, cols, rows)
	}
	screen, err := r.deps.Terminal.Snapshot(ctx, tabID)
	if err != nil {
		return Fail(err)
	}
	buffered, cursor := terminalInputOf(screen)
	r.state(jobID).observeInput(tabID, buffered, cursor)
	header := r.screenIdentityHeader(ctx, tabID) +
		fmt.Sprintf("光标 (%d, %d)，空闲 %dms，屏幕序号 %d", screen.CursorRow, screen.CursorCol, screen.IdleMS, screen.Seq)
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
	text := screen.Text
	if args.Unwrap {
		text = strings.Join(unwrapLines(strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), screen.Cols), "\n")
	}
	return OK(header + "\n" + text)
}

// screenIdentityHeader renders the per-tab identity lines (title, kind, cwd,
// handle) when the terminal exposes them; it degrades to empty otherwise.
func (r *Registry) screenIdentityHeader(ctx context.Context, tabID string) string {
	detail := r.tabDetail(ctx, tabID)
	if detail == (TabDetail{}) {
		return ""
	}
	line := "终端"
	if detail.Title != "" {
		line += fmt.Sprintf("「%s」", detail.Title)
	}
	if detail.Kind != "" {
		line += " kind=" + detail.Kind
	}
	if detail.CWD != "" {
		line += fmt.Sprintf(" cwd=%q", detail.CWD)
	}
	if detail.SessionID != "" {
		line += " handle=" + TerminalHandle{TabID: tabID, SessionID: detail.SessionID}.String()
	}
	return line + "\n"
}

// terminalInputOf extracts the observable input buffer state from a screen.
func terminalInputOf(screen Screen) (string, int) {
	return TerminalInputCursor(screen)
}

// readScreenHistory returns up to maxLines rendered scrollback lines
// directly above the visible screen, replayed from a bounded raw tail
// window. It reports the exact raw anchors of the window and the earliest
// retained sequence, so a reader needing more can continue forward with
// since_seq reads from earliest_seq.
func (r *Registry) readScreenHistory(ctx context.Context, tabID string, maxLines int, unwrap bool) Output {
	cols, rows := 80, 24
	if screen, snapshotErr := r.deps.Terminal.Snapshot(ctx, tabID); snapshotErr == nil {
		cols, rows = screen.Cols, screen.Rows
	}
	_, earliest, latest, err := r.outputSince(ctx, tabID, 0, 1)
	if err != nil {
		return Fail(err)
	}
	from := uint64(0)
	if latest > historyScanBytes {
		from = latest - historyScanBytes
	}
	data, start, windowLatest, err := r.outputSince(ctx, tabID, from, historyScanBytes)
	if err != nil {
		return Fail(err)
	}
	rendered, _ := renderOutputDiffSplit(data, cols, rows)
	scrollback := rendered
	if len(scrollback) > maxLines {
		scrollback = scrollback[len(scrollback)-maxLines:]
	}
	evicted := start > from || earliest > 0
	header := fmt.Sprintf("历史 %d 行（原始窗口 %d → %d，屏幕序号 %d）", len(scrollback), start, windowLatest, latest)
	if evicted {
		header += "；更早的输出已被淘汰"
	}
	header += fmt.Sprintf("；需要完整历史请用 since_seq=%d 向前翻页", earliest)
	lines := scrollback
	if unwrap {
		lines = unwrapLines(lines, cols)
	}
	text, truncated := capText(strings.Join(lines, "\n"))
	if truncated {
		header += "；本页渲染已截断，用更小的 history_lines 重读"
	}
	return Output{OK: true, Text: header + "\n" + text, Truncated: truncated}
}

// readScreenSince returns only the output produced after the anchor sequence,
// bounded by maxBytes. The caller supplies the geometry: cursor reads pass the
// one validated geometry, plain anchored reads pass their snapshot geometry or
// the 80x24 default. The header carries the exact continuation anchor
// (start+len(data)) plus an opaque cursor bound to that geometry and the tab's
// session identity, so the next page neither repeats nor skips output, plus
// explicit expired-anchor and render-truncation recovery notes.
func (r *Registry) readScreenSince(ctx context.Context, tabID string, sinceSeq uint64, maxBytes int, unwrap bool, cols, rows int) Output {
	data, start, latest, err := r.outputSince(ctx, tabID, sinceSeq, maxBytes)
	if err != nil {
		return Fail(err)
	}
	lines := renderOutputDiff(data, cols, rows)
	if unwrap {
		lines = unwrapLines(lines, cols)
	}
	next := start + uint64(len(data))
	header := fmt.Sprintf("新输出（序号 %d → %d，最新 %d）", start, next, latest)
	if start > sinceSeq {
		header += "；锚点已过期，改为读取最早保留的输出"
	}
	more := next < latest
	if more {
		header += fmt.Sprintf("；还有未读输出，下一页 since_seq=%d", next)
		// An identity resolver that exists but fails or yields an empty
		// identity must never produce an unbound cursor; the page still
		// continues with the plain anchor.
		hasResolver := r.hasTabSessionResolver()
		sessionID, sessionErr := r.tabSession(ctx, tabID)
		if hasResolver && (sessionErr != nil || sessionID == "") {
			header += "；身份解析失败或返回空身份，本次不提供 cursor，请用 since_seq 翻页"
		} else if cursor := encodeScreenCursor(screenCursor{Version: 1, Kind: "raw", Seq: next, TabID: tabID, SessionID: sessionID, Cols: cols, Rows: rows}); cursor != "" {
			header += " 或 cursor=" + cursor
		}
	}
	text, truncated := capText(strings.Join(lines, "\n"))
	if truncated {
		header += fmt.Sprintf("；本页渲染已截断，用 since_seq=%d 配合更小 max_bytes 重读", start)
	}
	return Output{OK: true, Text: header + "\n" + text, Truncated: truncated || more}
}

const (
	defaultSubmitDelayMS = 150
	maxSubmitDelayMS     = 10_000
)

// sendKeysArgs is the decoded argument set of send_keys, shared between the
// tool handler and the post-write input-baseline refresh.
type sendKeysArgs struct {
	Keys          string `json:"keys"`
	Enter         bool   `json:"enter"`
	WaitMS        int    `json:"wait_ms"`
	TabID         string `json:"tab_id"`
	Paste         bool   `json:"paste"`
	SubmitDelayMS int    `json:"submit_delay_ms"`
	Force         bool   `json:"force"`
	AllowControl  bool   `json:"allow_control"`
}

func (r *Registry) sendKeys(ctx context.Context, jobID string, scope Scope, call Call) Output {
	var args sendKeysArgs
	if err := decode(call.Args, &args); err != nil {
		return Fail(err)
	}
	if args.WaitMS < 0 || args.WaitMS > maxSendKeysWaitMS {
		return Fail(invalid("wait_ms 必须在 0-%d 毫秒之间", maxSendKeysWaitMS))
	}
	if args.SubmitDelayMS < 0 || args.SubmitDelayMS > maxSubmitDelayMS {
		return Fail(invalid("submit_delay_ms 必须在 0-%d 毫秒之间", maxSubmitDelayMS))
	}
	if args.SubmitDelayMS > 0 && !args.Paste {
		return Fail(invalid("submit_delay_ms 仅用于 paste 提交（paste=true 且 enter=true）"))
	}
	if args.Paste {
		if err := validatePasteText(args.Keys, args.AllowControl); err != nil {
			return Fail(err)
		}
	}
	tabID, err := r.resolveTab(ctx, jobID, scope, args.TabID)
	if err != nil {
		return Fail(err)
	}
	var encoded []byte
	if !args.Paste {
		encoded, err = EncodeKeys(args.Keys, args.Enter)
		if err != nil {
			return Fail(err)
		}
	}
	// User-input arbitration: refuse to write when the input buffer changed
	// after this job last observed it, unless the call forces through with
	// full knowledge. A failed snapshot degrades to an explicit note instead
	// of silently skipping the check.
	arbitrationNote := ""
	buffered, cursor := "", 0
	screen, snapshotErr := r.deps.Terminal.Snapshot(ctx, tabID)
	if snapshotErr != nil {
		arbitrationNote = "；终端输入状态无法校验（快照失败），已直接写入"
	} else {
		buffered, cursor = TerminalInputCursor(screen)
		if changed, observed := r.state(jobID).inputChanged(tabID, buffered, cursor); observed && changed {
			if !args.Force {
				return Fail(ErrUserInputPending)
			}
			arbitrationNote = "；force=true：已在知晓终端新输入的情况下写入"
		}
	}
	before, err := r.commandState(ctx, tabID)
	if err != nil {
		return Fail(err)
	}
	sameLockSubmit := true
	if args.Paste {
		delay := time.Duration(defaultSubmitDelayMS) * time.Millisecond
		if args.SubmitDelayMS > 0 {
			delay = time.Duration(args.SubmitDelayMS) * time.Millisecond
		}
		if submitter, ok := r.deps.Terminal.(PasteSubmitTerminal); ok {
			err = submitter.WritePasteSubmit(ctx, tabID, args.Keys, args.Enter, delay)
		} else {
			sameLockSubmit = false
			err = r.writePasteSplit(ctx, tabID, args.Keys, args.Enter, delay)
		}
	} else {
		err = r.deps.Terminal.Write(ctx, tabID, encoded)
	}
	if err != nil {
		// The write was initiated: its outcome is uncertain, so the failure
		// carries the same operation ID as a success would.
		result := Fail(err)
		result.OperationID = operationID(jobID, call.ID, "send_keys")
		return result
	}
	if r.deps.Audit != nil {
		_ = r.deps.Audit(context.WithoutCancel(ctx), AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: "takeover", Payload: map[string]any{"keys": "<redacted>", "enter": args.Enter, "paste": args.Paste, "tab": tabID}})
	}
	r.observePostWriteInput(jobID, tabID, buffered, cursor, args, encoded)
	var proofScreen Screen
	var proofErr error
	if args.Paste {
		proofScreen, proofErr = r.deps.Terminal.Snapshot(ctx, tabID)
	}
	var result Output
	if args.WaitMS == 0 {
		result = OK("已发送" + screenAnchorSuffix(ctx, r.deps.Terminal, tabID))
	} else if _, ok := r.deps.Terminal.(CommandStateTerminal); !ok {
		result = OK("已发送" + screenAnchorSuffix(ctx, r.deps.Terminal, tabID) + "；该终端不支持 OSC 133 命令跟踪，无法确认命令是否完成，请用 wait_for 校验新输出")
	} else {
		result = r.waitCommandFinish(ctx, tabID, before, args.WaitMS)
	}
	if args.Paste {
		result = withSubmissionProof(proofScreen, proofErr, result)
		if note := pasteLiteralNote(args.Keys); note != "" {
			result.Text += note
			result.Notes += note
		}
	}
	if !sameLockSubmit && args.Paste && args.Enter {
		result.Notes += "; paste and Enter were written as separate writes (terminal lacks the combined paste-submit lock); user input could interleave between them"
		result.Text += "；paste 与 Enter 为分次写入，之间可能插入用户输入"
	}
	result.Notes += arbitrationNote
	result.Text += arbitrationNote
	result.OperationID = operationID(jobID, call.ID, "send_keys")
	return result
}

// writePasteSplit is the fallback when the terminal cannot hold one write
// lock across the paste block, the submit delay, and the Enter key. The
// intermediate failure states are reported explicitly so the caller never
// blindly retries a partially submitted paste.
func (r *Registry) writePasteSplit(ctx context.Context, tabID, text string, enter bool, delay time.Duration) error {
	terminal, ok := r.deps.Terminal.(PasteTerminal)
	if !ok {
		return errors.New("该终端不支持原子 paste 写入")
	}
	if err := terminal.WritePaste(ctx, tabID, text); err != nil {
		return err
	}
	if !enter {
		return nil
	}
	timer := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		_ = timer.Stop()
		return fmt.Errorf("paste 块已写入，Enter 未发送；请检查屏幕且不要盲重发: %w", ctx.Err())
	case <-timer.C:
	}
	if err := r.deps.Terminal.Write(ctx, tabID, []byte("\r")); err != nil {
		return fmt.Errorf("paste 块已写入，但 Enter 发送失败；请检查屏幕且不要盲重发: %w", err)
	}
	return nil
}

// observePostWriteInput refreshes the job's input baseline after its own
// write: deterministic text/paste writes predict the resulting buffer, and
// unpredictable control keys fall back to the live post-write screen so new
// user input still trips the next arbitration check.
func (r *Registry) observePostWriteInput(jobID, tabID, buffered string, cursor int, args sendKeysArgs, encoded []byte) {
	state := r.state(jobID)
	if args.Paste {
		if args.Enter {
			state.observeInput(tabID, "", 0)
			return
		}
		if predicted, predictedCursor, ok := predictedInput(buffered, cursor, []byte(args.Keys)); ok {
			state.observeInput(tabID, predicted, predictedCursor)
			return
		}
	} else if predicted, predictedCursor, ok := predictedInput(buffered, cursor, encoded); ok {
		state.observeInput(tabID, predicted, predictedCursor)
		return
	}
	if screen, err := r.deps.Terminal.Snapshot(context.Background(), tabID); err == nil {
		observed, observedCursor := TerminalInputCursor(screen)
		state.observeInput(tabID, observed, observedCursor)
	}
}

func withSubmissionProof(screen Screen, snapshotErr error, result Output) Output {
	result.WriteConfirmed = true
	result.Notes = "write_confirmed acknowledges the terminal input writes only; submitted_seq is the post-write terminal output sequence anchor, not proof that the remote application accepted a turn"
	if snapshotErr == nil {
		seq := screen.Seq
		result.SubmittedSeq = &seq
		result.Text += fmt.Sprintf("（submitted_seq=%d）", seq)
	} else {
		result.Text += "；底层输入写入已确认，但输出序号不可用"
		result.Notes += "; the output sequence could not be read after the acknowledged writes"
	}
	result.Text += "；submitted_seq 是写入后的终端输出序号锚点，不代表远端程序已受理"
	return result
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

func (r *Registry) terminalCreate(ctx context.Context, jobID string, scope Scope, call Call) Output {
	var args struct {
		Title      string `json:"title"`
		Command    string `json:"command"`
		Background bool   `json:"background"`
		AssetID    string `json:"asset_id"`
		Connect    bool   `json:"connect"`
	}
	if err := decode(call.Args, &args); err != nil {
		return Fail(err)
	}
	sessionID := scope.SessionID
	connected := false
	operation := operationID(jobID, call.ID, "terminal_create")
	if args.AssetID != "" {
		if strings.TrimSpace(args.AssetID) != args.AssetID {
			return Fail(invalid("asset_id 不能包含首尾空白"))
		}
		if r.deps.ResolveAssetSession == nil {
			return Fail(errors.New("按资产创建终端未配置"))
		}
		resolved, err := r.deps.ResolveAssetSession(ctx, args.AssetID)
		if err != nil {
			// Connecting is an uncertain mutation that leaves a residual
			// session behind, so it happens only on explicit opt-in and only
			// for an explicit not-connected verdict; forbidden, unsupported,
			// and transient resolver failures propagate untouched.
			if !args.Connect || !errors.Is(err, ErrNoLiveSession) {
				return Fail(err)
			}
			if r.deps.AssetConnector == nil {
				return Fail(fmt.Errorf("解析资产会话失败: %v；未配置资产连接建立（connect=true 需要环境支持）", err))
			}
			connectedID, connectErr := r.deps.AssetConnector.ConnectAsset(ctx, args.AssetID)
			if connectErr != nil {
				failure := Fail(fmt.Errorf("解析资产会话失败: %v；建立新连接失败: %w", err, connectErr))
				if connectedID != "" {
					failure.Text += fmt.Sprintf("；已建立的会话 session_id=%s 作为残留资源保留（未自动清理）", connectedID)
				}
				failure.OperationID = operation
				return failure
			}
			sessionID = connectedID
			connected = true
		} else {
			sessionID = resolved
		}
	}
	if sessionID == "" {
		return Fail(errors.New("当前 AI 作用域没有 sessionId"))
	}
	terminal, ok := r.deps.Terminal.(CreateTerminal)
	if !ok {
		return Fail(errors.New("终端服务未配置"))
	}
	created, err := terminal.Create(ctx, sessionID, CreateTerminalOptions{Title: args.Title, Command: args.Command})
	if err != nil {
		failure := Fail(err)
		if connected {
			failure.Text = fmt.Sprintf("标签页创建失败: %v；已建立的连接 session_id=%s 作为残留资源保留（未自动清理）；请先核对终端与连接状态再决定后续操作", err, sessionID)
		}
		// The create was initiated: its outcome is uncertain, so the failure
		// carries the same operation ID as a success would.
		failure.OperationID = operation
		return failure
	}
	r.state(jobID).rememberCreatedTab(created.TabID, sessionID)
	handle := TerminalHandle{TabID: created.TabID, SessionID: sessionID}.String()
	detail := fmt.Sprintf("tab_id=%s", created.TabID)
	if args.AssetID != "" {
		detail += fmt.Sprintf("，asset_id=%s", args.AssetID)
	}
	if connected {
		detail += fmt.Sprintf("，已建立新连接 session_id=%s", sessionID)
	}
	if created.Title != "" {
		detail += fmt.Sprintf("，标题「%s」", created.Title)
	}
	detail += "，handle=" + handle
	if args.Command != "" {
		detail += "，启动命令已发送"
	}
	if args.Background {
		return Output{OK: true, Text: fmt.Sprintf("已在后台创建分离终端（%s），AI 可在其中工作，用户可在「后台会话」中接管", detail), OperationID: operation}
	}
	return Output{OK: true, Text: fmt.Sprintf("已创建并在前台展示终端标签页（%s），AI 正在其中工作，用户可随时接管输入", detail), OperationID: operation,
		Terminal: &CreatedTerminal{TabID: created.TabID, SessionID: sessionID, Title: created.Title, Handle: handle}}
}

func (r *Registry) terminalRename(ctx context.Context, jobID string, scope Scope, raw []byte) Output {
	var args struct {
		TabID string `json:"tab_id"`
		Title string `json:"title"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	tabID, err := r.resolveTab(ctx, jobID, scope, args.TabID)
	if err != nil {
		return Fail(err)
	}
	terminal, ok := r.deps.Terminal.(RenameTerminal)
	if !ok {
		return Fail(errors.New("终端服务未配置"))
	}
	if err := terminal.Rename(ctx, tabID, args.Title); err != nil {
		return Fail(err)
	}
	return OK(fmt.Sprintf("已将终端标签页 %s 重命名为「%s」", tabID, args.Title))
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
			return r.commandFinishOutput(ctx, tabID, state, busySeq, "发送前已有命令在运行，该命令仍在运行（本次按键已送入该程序）")
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

func (r *Registry) waitFor(ctx context.Context, jobID string, scope Scope, raw []byte, maximumSeconds int) Output {
	var args struct {
		Pattern   *string  `json:"pattern"`
		TimeoutMS int64    `json:"timeout_ms"`
		SinceSeq  uint64   `json:"since_seq"`
		TabID     string   `json:"tab_id"`
		QuietMS   int64    `json:"quiet_ms"`
		TabIDs    []string `json:"tab_ids"`
		Mode      string   `json:"mode"`
		Lines     int      `json:"lines"`
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
	if args.QuietMS < 0 || args.QuietMS > int64(maximumSeconds)*1000 {
		return Fail(invalid("quiet_ms 超出 0-%d 毫秒范围", maximumSeconds*1000))
	}
	if args.Lines < 0 || args.Lines > maxWaitLines {
		return Fail(invalid("lines 必须在 0-%d 之间", maxWaitLines))
	}
	if args.Mode != "" && args.Mode != "any" && args.Mode != "all" {
		return Fail(invalid("mode 必须为 any 或 all"))
	}
	if args.Pattern == nil && args.QuietMS == 0 && args.Lines == 0 {
		return Fail(invalid("pattern 不能为空；仅 quiet_ms 大于 0 或 lines 大于 0 时可以省略"))
	}
	if args.TabID != "" && len(args.TabIDs) > 0 {
		return Fail(invalid("tab_id 与 tab_ids 不能同时使用"))
	}
	if len(args.TabIDs) == 1 {
		args.TabID = args.TabIDs[0]
		args.TabIDs = nil
	}
	pattern := ""
	if args.Pattern != nil {
		pattern = *args.Pattern
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return Fail(invalid("正则无效: %v", err))
	}
	if args.Lines > 0 || len(args.TabIDs) > 1 {
		mode := args.Mode
		if mode == "" {
			mode = "all"
		}
		tabIDs := args.TabIDs
		if len(tabIDs) == 0 {
			tabIDs = []string{args.TabID}
		}
		waitContext, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
		defer cancel()
		var matcher *regexp.Regexp
		if pattern != "" {
			matcher = regex
		}
		return r.waitForTabs(waitContext, jobID, scope, tabIDs, matcher, pattern, args.Lines, args.QuietMS, mode, args.SinceSeq)
	}
	tabID, err := r.resolveTab(ctx, jobID, scope, args.TabID)
	if err != nil {
		return Fail(err)
	}
	if args.QuietMS > 0 {
		if _, ok := r.deps.Terminal.(WaitSnapshotTerminal); !ok {
			return Fail(errors.New("该终端不支持同步安静快照"))
		}
	}
	waitContext, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
	defer cancel()
	if args.SinceSeq > 0 {
		return r.waitForNewOutput(waitContext, tabID, regex, args.SinceSeq, args.QuietMS)
	}
	ticker := time.NewTicker(r.deps.pollInterval())
	defer ticker.Stop()
	for {
		screen, snapshotErr := r.waitSnapshot(waitContext, tabID, args.QuietMS > 0)
		if snapshotErr != nil {
			// The wait budget exhausted mid-snapshot is the wait's own
			// timeout: surface the structured timeout output, not a raw
			// context error.
			if waitContext.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				return legacyWaitTimeoutOutput(tabID, nil, Screen{})
			}
			return Fail(snapshotErr)
		}
		tail := screen.Tail
		if len(tail) == 0 {
			tail = strings.Split(screen.Text, "\n")
		}
		if len(tail) > 30 {
			tail = tail[len(tail)-30:]
		}
		if regex.MatchString(strings.Join(tail, "\n")) && (args.QuietMS == 0 || screen.IdleMS >= args.QuietMS) {
			output := OK(waitSatisfiedText(pattern, screen.IdleMS, args.QuietMS))
			output.Wait = &WaitOutcome{Mode: "single", Satisfied: true, Tabs: []WaitTabOutcome{{
				TabID: tabID, Matched: true, IdleMS: screen.IdleMS, ToSeq: screen.Seq, Note: "匹配自可见屏幕尾部，无输出锚点",
			}}}
			return output
		}
		select {
		case <-waitContext.Done():
			if ctx.Err() != nil {
				return Fail(ctx.Err())
			}
			return legacyWaitTimeoutOutput(tabID, tail, screen)
		case <-ticker.C:
		}
	}
}

func legacyWaitTimeoutOutput(tabID string, tail []string, screen Screen) Output {
	if len(tail) > 10 {
		tail = tail[len(tail)-10:]
	}
	return Output{Text: "等待超时，最近输出：\n" + strings.Join(tail, "\n"), ExitCode: 1,
		Wait: &WaitOutcome{Mode: "single", TimedOut: true, Tabs: []WaitTabOutcome{{
			TabID: tabID, IdleMS: screen.IdleMS, ToSeq: screen.Seq, Note: "匹配自可见屏幕尾部，无输出锚点",
		}}}}
}

func waitSatisfiedText(pattern string, idleMS, quietMS int64) string {
	if quietMS == 0 {
		return "模式已出现"
	}
	if pattern == "" {
		return fmt.Sprintf("终端已安静 %dms（要求 %dms）", idleMS, quietMS)
	}
	return fmt.Sprintf("模式已出现，终端已安静 %dms（要求 %dms）", idleMS, quietMS)
}

func (r *Registry) waitSnapshot(ctx context.Context, tabID string, synchronized bool) (Screen, error) {
	if synchronized {
		return r.deps.Terminal.(WaitSnapshotTerminal).WaitSnapshot(ctx, tabID)
	}
	return r.deps.Terminal.Snapshot(ctx, tabID)
}

// waitForNewOutput matches only output produced after the anchor sequence, so
// content that was already on screen can never false-match. The scan cursor
// advances through the new output in bounded windows, carrying an overlap so
// patterns spanning a window seam still match; every byte after the anchor is
// scanned at least once (evicted bytes are flagged in the timeout output).
func (r *Registry) waitForNewOutput(waitContext context.Context, tabID string, regex *regexp.Regexp, sinceSeq uint64, quietMS int64) Output {
	cols, rows := 80, 24
	if screen, err := r.waitSnapshot(waitContext, tabID, quietMS > 0); err == nil {
		cols, rows = screen.Cols, screen.Rows
	}
	ticker := time.NewTicker(r.deps.pollInterval())
	defer ticker.Stop()
	scanPos := sinceSeq
	var carry []byte
	var recent []string
	gap := false
	matched := false
	toSeq := sinceSeq
	for {
		data, start, latest, err := r.outputSince(waitContext, tabID, scanPos, waitScanBytes)
		if err != nil {
			return Fail(err)
		}
		toSeq = latest
		if start > scanPos {
			gap = true
		}
		window := append(carry, data...)
		rendered := renderOutputDiff(window, cols, rows)
		matched = matched || regex.MatchString(strings.Join(rendered, "\n"))
		if matched && quietMS == 0 {
			output := OK(fmt.Sprintf("模式已出现（新输出，屏幕序号 %d）", latest))
			output.Wait = &WaitOutcome{Mode: "single", Satisfied: true, Tabs: []WaitTabOutcome{{
				TabID: tabID, Matched: true, FromSeq: sinceSeq, ToSeq: latest,
			}}}
			return output
		}
		if matched && quietMS > 0 {
			screen, snapshotErr := r.waitSnapshot(waitContext, tabID, quietMS > 0)
			if snapshotErr != nil {
				return Fail(snapshotErr)
			}
			if screen.IdleMS >= quietMS {
				output := OK(fmt.Sprintf("%s（新输出，屏幕序号 %d）", waitSatisfiedText(regex.String(), screen.IdleMS, quietMS), latest))
				output.Wait = &WaitOutcome{Mode: "single", Satisfied: true, Tabs: []WaitTabOutcome{{
					TabID: tabID, Matched: true, IdleMS: screen.IdleMS, FromSeq: sinceSeq, ToSeq: latest,
				}}}
				return output
			}
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
				note := ""
				if gap {
					note = "部分输出已被淘汰"
				}
				return Output{Text: text + "：\n" + strings.Join(shown, "\n"), ExitCode: 1,
					Wait: &WaitOutcome{Mode: "single", TimedOut: true, Tabs: []WaitTabOutcome{{
						TabID: tabID, FromSeq: sinceSeq, ToSeq: toSeq, Note: note,
					}}}}
			}
			return Fail(waitContext.Err())
		case <-ticker.C:
		}
	}
}
