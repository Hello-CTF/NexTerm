package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	shellHistoryTailBytes    = 8 << 20
	shellHistoryDefaultLimit = 50
	shellHistoryMaxLimit     = 500
)

// shellHistoryKind is the guard approval kind for shell_history confirmation
// memory (allow_session). It lives here so the guard package needs no new
// constant for a single tool.
const shellHistoryKind = "shell_history"

type ShellHistoryArgs struct {
	Limit int `json:"limit,omitempty"`
}

var (
	zshHistoryLine   = regexp.MustCompile(`^:\s*\d+:\d+;`)
	bashHistoryStamp = regexp.MustCompile(`^#\d+\s*$`)
	fishHistoryCmd   = regexp.MustCompile(`^-\s+cmd:\s+(.*)$`)
)

type shellHistoryCandidate struct {
	shell string
	path  string
}

func shellHistoryPaths() []shellHistoryCandidate {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []shellHistoryCandidate{
		{shell: "zsh", path: filepath.Join(home, ".zsh_history")},
		{shell: "bash", path: filepath.Join(home, ".bash_history")},
		{shell: "fish", path: filepath.Join(home, ".local", "share", "fish", "fish_history")},
	}
}

func (r *Registry) shellHistory(ctx context.Context, scope Scope, raw []byte) Output {
	var args ShellHistoryArgs
	if err := decode(raw, &args); err != nil {
		return Fail(err)
	}
	if args.Limit == 0 {
		args.Limit = shellHistoryDefaultLimit
	}
	if args.Limit < 1 {
		return Fail(invalid("limit 必须为正整数"))
	}
	if args.Limit > shellHistoryMaxLimit {
		args.Limit = shellHistoryMaxLimit
	}
	entries, shell, err := readLocalShellHistory(args.Limit)
	if err != nil {
		return Fail(err)
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, RedactText(entry))
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		text = "（历史为空）"
	}
	text, truncated := capText(text)
	prefix := fmt.Sprintf("[本机 %s shell 历史，最新 %d 条]\n", shell, len(entries))
	if r.scopeRemoteAsset(ctx, scope) {
		prefix = "[注意] 当前会话是远端资产；以下为 NexTerm 所在本机的 shell 历史，不是远端机器的历史。\n" + prefix
	}
	return Output{OK: true, Text: prefix + text, Truncated: truncated}
}

// scopeRemoteAsset reports whether the scope's asset is a remote one; local
// shell history must be labeled explicitly in that case.
func (r *Registry) scopeRemoteAsset(ctx context.Context, scope Scope) bool {
	if scope.AssetID == "" || r.deps.ListAssets == nil {
		return false
	}
	assets, err := r.deps.ListAssets(ctx)
	if err != nil {
		return false
	}
	for _, asset := range assets {
		if asset.ID == scope.AssetID {
			return asset.Kind != "local"
		}
	}
	return false
}

// readLocalShellHistory reads the newest limit entries from the first existing
// local shell history file (zsh, then bash, then fish). At most
// shellHistoryTailBytes are read from the file tail, and every entry is
// redacted by the caller.
func readLocalShellHistory(limit int) ([]string, string, error) {
	candidates := shellHistoryPaths()
	var attempted []string
	for _, candidate := range candidates {
		attempted = append(attempted, candidate.path)
		info, err := os.Stat(candidate.path)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := readFileTail(candidate.path, shellHistoryTailBytes)
		if err != nil {
			return nil, "", fmt.Errorf("读取 %s 历史失败: %w", candidate.shell, err)
		}
		entries := parseShellHistory(candidate.shell, data)
		if len(entries) > limit {
			entries = entries[len(entries)-limit:]
		}
		for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
			entries[left], entries[right] = entries[right], entries[left]
		}
		return entries, candidate.shell, nil
	}
	if len(attempted) == 0 {
		return nil, "", errors.New("无法确定本机用户目录，未读取 shell 历史")
	}
	return nil, "", fmt.Errorf("未找到本机 shell 历史文件（已尝试: %s）", strings.Join(attempted, "、"))
}

// readFileTail reads at most max bytes from the end of the file. When the
// file is larger, the leading partial line is dropped so entries start at a
// line boundary.
func readFileTail(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > max {
		offset = info.Size() - max
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, max))
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			data = nil
		}
	}
	return data, nil
}

// parseShellHistory extracts command entries from a history file body,
// following the per-shell on-disk formats: zsh extended ": <ts>:<ts>;" lines,
// bash plain lines with optional "#<ts>" timestamp comments, and fish
// "- cmd: <command>" yaml lines.
func parseShellHistory(shell string, data []byte) []string {
	entries := make([]string, 0, 256)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		switch shell {
		case "zsh":
			if loc := zshHistoryLine.FindStringIndex(line); loc != nil {
				entries = append(entries, line[loc[1]:])
			}
		case "bash":
			if line == "" || bashHistoryStamp.MatchString(line) {
				continue
			}
			entries = append(entries, line)
		case "fish":
			if match := fishHistoryCmd.FindStringSubmatch(line); match != nil {
				entries = append(entries, match[1])
			}
		}
	}
	return entries
}
