package tools

import "strings"

func TerminalInputBuffer(screen Screen) string {
	buffered, _ := TerminalInputCursor(screen)
	return buffered
}

func TerminalInputCursor(screen Screen) (string, int) {
	if screen.Text == "" {
		return "", 0
	}
	lines := strings.Split(strings.ReplaceAll(screen.Text, "\r\n", "\n"), "\n")
	row := screen.CursorRow
	if row < 0 || row >= len(lines) {
		row = len(lines) - 1
		for row > 0 && lines[row] == "" {
			row--
		}
	}
	lines[row] = strings.TrimSuffix(lines[row], "\r")
	start := 0
	for index := row; index >= 0; index-- {
		if terminalPrompt(lines[index]) {
			start = index
			break
		}
	}
	parts := append([]string(nil), lines[start:row+1]...)
	if len(parts) == 0 {
		return "", 0
	}
	rawCursor := 0
	for _, part := range parts[:len(parts)-1] {
		rawCursor += len([]rune(part))
	}
	rawCursor += screen.CursorCol
	before := len([]rune(parts[0]))
	parts[0] = strings.TrimLeft(parts[0], " \t")
	removed := before - len([]rune(parts[0]))
	for _, prompt := range []string{"$ ", "# ", "% "} {
		if strings.HasPrefix(parts[0], prompt) {
			removed += len([]rune(prompt))
			parts[0] = strings.TrimPrefix(parts[0], prompt)
			break
		}
	}
	buffered := strings.Join(parts, "")
	length := len([]rune(buffered))
	cursor := rawCursor - removed
	if cursor < 0 {
		cursor = 0
	}
	if cursor > length {
		cursor = length
	}
	return buffered, cursor
}

func terminalPrompt(line string) bool {
	line = strings.TrimLeft(line, " \t")
	for _, prompt := range []string{"$ ", "# ", "% "} {
		if strings.HasPrefix(line, prompt) {
			return true
		}
	}
	return false
}
