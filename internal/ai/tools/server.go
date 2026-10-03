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

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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

func (r *Registry) readScreen(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		TabID string `json:"tab_id"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	tabID, err := r.resolveTab(scope, args.TabID)
	if err != nil {
		return Fail(err)
	}
	screen, err := r.deps.Terminal.Snapshot(ctx, tabID)
	if err != nil {
		return Fail(err)
	}
	return OK(fmt.Sprintf("光标 (%d, %d)，空闲 %dms\n%s", screen.CursorRow, screen.CursorCol, screen.IdleMS, screen.Text))
}

func (r *Registry) sendKeys(ctx context.Context, scope Scope, raw []byte) Output {
	var args struct {
		Keys  string `json:"keys"`
		Enter bool   `json:"enter"`
	}
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	tabID, err := r.resolveTab(scope, "")
	if err != nil {
		return Fail(err)
	}
	encoded, err := EncodeKeys(args.Keys, args.Enter)
	if err != nil {
		return Fail(err)
	}
	if err := r.deps.Terminal.Write(ctx, tabID, encoded); err != nil {
		return Fail(err)
	}
	if r.deps.Audit != nil {
		_ = r.deps.Audit(context.WithoutCancel(ctx), AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: "takeover", Payload: map[string]any{"keys": "<redacted>", "enter": args.Enter, "tab": tabID}})
	}
	return OK("已发送")
}

func (r *Registry) waitFor(ctx context.Context, scope Scope, raw []byte, maximumSeconds int) Output {
	var args struct {
		Pattern   string `json:"pattern"`
		TimeoutMS int64  `json:"timeout_ms"`
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
