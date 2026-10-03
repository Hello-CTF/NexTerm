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
	parts := append([]string(nil), lines[start:]...)
	if len(parts) == 0 {
		return "", 0
	}
	rawCursor := 0
	for index := start; index < row; index++ {
		rawCursor += len([]rune(lines[index]))
	}
	rawCursor += screen.CursorCol
	before := len([]rune(parts[0]))
	parts[0] = strings.TrimLeft(parts[0], " \t")
	removed := before - len([]rune(parts[0]))
	if prompt := terminalPromptPrefix(parts[0]); prompt > 0 {
		removed += prompt
		parts[0] = string([]rune(parts[0])[prompt:])
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
	return terminalPromptPrefix(line) > 0
}

func terminalPromptPrefix(line string) int {
	line = strings.TrimLeft(line, " \t")
	for _, prompt := range []string{"$ ", "# ", "% "} {
		if strings.HasPrefix(line, prompt) {
			return len([]rune(prompt))
		}
	}
	runes := []rune(line)
	for index, r := range runes {
		if (r == '$' || r == '#' || r == '%') && index+1 < len(runes) && runes[index+1] == ' ' {
			prefix := string(runes[:index])
			if strings.Contains(prefix, "@") || strings.Contains(prefix, ":") {
				return index + 2
			}
		}
	}
	return 0
}
