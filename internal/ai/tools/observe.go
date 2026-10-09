package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

const (
	defaultDiffBytes = 8 << 10
	waitScanBytes    = 64 << 10
	waitScanOverlap  = 8 << 10
	diffHistoryLines = 400
)

// renderOutputDiff replays raw terminal bytes through a throwaway screen
// emulator so anchored reads and waits see clean text instead of escape
// sequences. The anchor may cut mid-sequence; the leading fragment renders
// verbatim, which is bounded and cosmetic.
func renderOutputDiff(data []byte, cols, rows int) []string {
	if len(data) == 0 {
		return nil
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	tab := terminal.NewTab("diff", "", cols, rows, terminal.UTF8, terminal.WithScreenHistory(diffHistoryLines))
	tab.Feed(data)
	lines := tab.ScreenHistory(diffHistoryLines)
	lines = append(lines, strings.Split(tab.ScreenText(), "\n")...)
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed = append(trimmed, strings.TrimRight(line, " \t\r"))
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// outputSince reads raw tab output produced after seq via the optional
// terminal capability.
func (r *Registry) outputSince(ctx context.Context, tabID string, seq uint64, maxBytes int) ([]byte, uint64, uint64, error) {
	if terminal, ok := r.deps.Terminal.(OutputSinceTerminal); ok {
		return terminal.OutputSince(ctx, tabID, seq, maxBytes)
	}
	return nil, 0, 0, errors.New("该终端不支持锚定输出读取")
}
