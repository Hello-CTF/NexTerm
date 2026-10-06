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
	if r.remoteAsset(ctx, scope) {
		prefix = "[注意] 当前会话是远端资产；以下为 NexTerm 所在本机的 shell 历史，不是远端机器的历史。\n" + prefix
	}
	return Output{OK: true, Text: prefix + text, Truncated: truncated}
}

// remoteAsset reports whether the scope's session is remote. The asset kind
// is resolved on the server from the tab/session, never from the
// client-supplied AssetID alone, and it fails closed: a session or tab whose
// kind cannot be determined counts as remote. Only a scope without any
// session context (a plain local chat) is treated as local.
func (r *Registry) remoteAsset(ctx context.Context, scope Scope) bool {
	sessionID := scope.SessionID
	if sessionID == "" && scope.TabID != "" && r.deps.TabSession != nil {
		sessionID = r.deps.TabSession(scope.TabID)
	}
	if sessionID != "" {
		if terminal, ok := r.deps.Terminal.(AssetKindTerminal); ok {
			kind, err := terminal.SessionAssetKind(ctx, sessionID)
			if err != nil || kind == "" {
				return true
			}
			return kind != "local"
		}
		return true
	}
	if scope.AssetID == "" {
		return false
	}
	if r.deps.ListAssets == nil {
		return true
	}
	assets, err := r.deps.ListAssets(ctx)
	if err != nil {
		return true
	}
	for _, asset := range assets {
		if asset.ID == scope.AssetID {
			return asset.Kind != "local"
		}
	}
	return true
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

// parseShellHistory extracts command entries from a history file body.
// zsh files are detected per file: when any extended ": <ts>:<ts>;" line is
// present only those lines count (continuation lines are skipped); a plain
// zsh file yields every non-empty line. bash skips "#<ts>" timestamp
// comments; fish takes "- cmd: <command>" yaml lines.
func parseShellHistory(shell string, data []byte) []string {
	lines := strings.Split(string(data), "\n")
	entries := make([]string, 0, 256)
	switch shell {
	case "zsh":
		extended := false
		for _, line := range lines {
			if zshHistoryLine.MatchString(strings.TrimRight(line, "\r")) {
				extended = true
				break
			}
		}
		for _, line := range lines {
			line = strings.TrimRight(line, "\r")
			if loc := zshHistoryLine.FindStringIndex(line); loc != nil {
				entries = append(entries, line[loc[1]:])
			} else if !extended && strings.TrimSpace(line) != "" {
				entries = append(entries, line)
			}
		}
	case "bash":
		for _, line := range lines {
			line = strings.TrimRight(line, "\r")
			if line == "" || bashHistoryStamp.MatchString(line) {
				continue
			}
			entries = append(entries, line)
		}
	case "fish":
		for _, line := range lines {
			if match := fishHistoryCmd.FindStringSubmatch(strings.TrimRight(line, "\r")); match != nil {
				entries = append(entries, match[1])
			}
		}
	}
	return entries
}
