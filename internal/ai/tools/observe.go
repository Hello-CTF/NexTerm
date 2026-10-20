package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
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
	scrollback, screen := renderOutputDiffSplit(data, cols, rows)
	return append(scrollback, screen...)
}

// renderOutputDiffSplit renders like renderOutputDiff but keeps the
// scrollback lines and the visible screen lines separate, so callers can
// address deep history without guessing where the screen begins.
func renderOutputDiffSplit(data []byte, cols, rows int) ([]string, []string) {
	if len(data) == 0 {
		return nil, nil
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	tab := terminal.NewTab("diff", "", cols, rows, terminal.UTF8, terminal.WithScreenHistory(diffHistoryLines))
	tab.Feed(data)
	return trimRenderedLines(tab.ScreenHistory(diffHistoryLines)), trimRenderedLines(strings.Split(tab.ScreenText(), "\n"))
}

func trimRenderedLines(lines []string) []string {
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

// historyScanBytes bounds the raw window replayed for a deep-history read.
const historyScanBytes = 256 << 10

// unwrapLines joins soft-wrapped screen lines back into logical lines: a
// rendered line that filled the full terminal width continues on the next
// line. Long logical lines that happen to end exactly at the width boundary
// are joined with their successor; that ambiguity is inherent to wrapped
// screen content.
func unwrapLines(lines []string, cols int) []string {
	if cols <= 0 || len(lines) == 0 {
		return lines
	}
	joined := make([]string, 0, len(lines))
	var current strings.Builder
	for _, line := range lines {
		current.WriteString(line)
		if len([]rune(line)) >= cols {
			continue
		}
		joined = append(joined, current.String())
		current.Reset()
	}
	if current.Len() > 0 {
		joined = append(joined, current.String())
	}
	return joined
}

// screenCursor is the opaque continuation token for anchored raw-stream
// reads. It binds the anchor to the exact tab and session it was issued
// for, and to the terminal geometry at issuance: a cursor must never read
// another terminal's stream or page with shifted wrapping after a resize.
type screenCursor struct {
	Version   int    `json:"v"`
	Kind      string `json:"kind"`
	Seq       uint64 `json:"seq"`
	TabID     string `json:"tab"`
	SessionID string `json:"session,omitempty"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

func encodeScreenCursor(cursor screenCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeScreenCursor(value string) (screenCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return screenCursor{}, invalid("游标无法解码")
	}
	var cursor screenCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return screenCursor{}, invalid("游标无法解析")
	}
	if cursor.Version != 1 || cursor.Kind != "raw" {
		return screenCursor{}, invalid("游标版本或类型不受支持")
	}
	return cursor, nil
}

// validatePasteText enforces the literal-paste contract: paste content is
// delivered verbatim, so a carriage return would submit outside the intended
// enter=true flow, and control characters would reach the terminal as raw
// bytes rather than text. Models must route submission through enter and
// acknowledge deliberate control bytes explicitly.
func validatePasteText(text string, allowControl bool) error {
	if strings.ContainsRune(text, '\r') {
		return invalid("paste 内容包含回车符（\\r）；请移除并使用 enter=true 提交，避免粘贴内容提前执行")
	}
	if !allowControl {
		for _, r := range text {
			if r == '\n' || r == '\t' {
				continue
			}
			if r < 0x20 || r == 0x7f {
				return invalid("paste 内容包含控制字符（U+%04X）；确认需要原样发送时请设置 allow_control=true", r)
			}
		}
	}
	return nil
}

var keyTagPattern = regexp.MustCompile(`<[a-zA-Z][a-zA-Z0-9+_-]{0,15}>`)

// pasteLiteralNote warns when paste content looks like key tags: paste sends
// text verbatim, so "<enter>" would arrive as those literal characters.
func pasteLiteralNote(text string) string {
	if keyTagPattern.MatchString(text) {
		return "；paste 内容按字面发送，<...> 不会被解析为按键"
	}
	return ""
}

// predictedInput computes the terminal input buffer expected after writing
// encoded keys: pure text inserts at the cursor, a trailing carriage return
// submits and clears the line. ok is false when the keys contain control
// sequences whose editing effect cannot be predicted locally.
func predictedInput(buffered string, cursor int, encoded []byte) (string, int, bool) {
	submit := false
	body := encoded
	if len(body) > 0 && body[len(body)-1] == '\r' {
		submit = true
		body = body[:len(body)-1]
	}
	for _, b := range body {
		if b < 0x20 || b == 0x7f {
			return "", 0, false
		}
	}
	if submit {
		return "", 0, true
	}
	runes := []rune(buffered)
	if cursor < 0 || cursor > len(runes) {
		cursor = len(runes)
	}
	insert := []rune(string(body))
	merged := make([]rune, 0, len(runes)+len(insert))
	merged = append(merged, runes[:cursor]...)
	merged = append(merged, insert...)
	merged = append(merged, runes[cursor:]...)
	return string(merged), cursor + len(insert), true
}
