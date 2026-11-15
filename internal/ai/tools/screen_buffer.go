package tools

import (
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// TerminalInputCursor returns the input buffer the guard classifies against:
// everything from the last prompt row at or above the cursor (or the whole
// screen when no prompt is recognized) and the cursor as a rune offset into
// that buffer. It deliberately over-approximates, so text already on screen
// is never left out of a submission check.
func TerminalInputCursor(screen Screen) (string, int) {
	lines, row, ok := screenInputLines(screen)
	if !ok {
		return "", 0
	}
	start := 0
	for index := row; index >= 0; index-- {
		if terminalPrompt(lines[index]) {
			start = index
			break
		}
	}
	return inputBuffer(lines, start, len(lines), row, screen.CursorCol, terminalPromptPrefix, false)
}

// arbitrationInput returns the user-editable input for the pending-input
// check, and false when the screen has no recognizable shell input line: the
// alternate screen, a running command, or a cursor row that is neither a
// prompt row nor a soft-wrapped continuation of one. Program output such as
// a progress line must never count as unseen user input.
func arbitrationInput(screen Screen, running bool) (string, int, bool) {
	if running || screen.AltScreen {
		return "", 0, false
	}
	lines, row, ok := screenInputLines(screen)
	if !ok {
		return "", 0, false
	}
	start := -1
	for index := row; index >= 0; index-- {
		if inputPromptPrefix(lines[index]) > 0 {
			start = index
			break
		}
		if index == 0 || !softWrapped(lines[index-1], screen.Cols) {
			break
		}
	}
	if start < 0 {
		return "", 0, false
	}
	end := row + 1
	for end < len(lines) && softWrapped(lines[end-1], screen.Cols) {
		end++
	}
	buffered, cursor := inputBuffer(lines, start, end, row, screen.CursorCol, inputPromptPrefix, true)
	return buffered, cursor, true
}

// unobservedInputCursor marks an arbitration baseline taken while the screen
// had no comparable input line.
const unobservedInputCursor = -1

// observeArbitrationInput records the input baseline the job saw on screen.
func (r *Registry) observeArbitrationInput(jobID, tabID string, screen Screen, running bool) {
	buffered, cursor, ok := arbitrationInput(screen, running)
	if !ok {
		buffered, cursor = "", unobservedInputCursor
	}
	r.state(jobID).observeInput(tabID, buffered, cursor)
}

// pendingInput reports whether the live input line holds input the job has
// not observed. Without a baseline nothing is compared; a baseline taken
// while no input line was visible only accepts an empty input line.
func (s *jobState) pendingInput(tabID, buffered string, cursor int) bool {
	s.mu.Lock()
	baseline, ok := s.lastInput[tabID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	if baseline.cursor == unobservedInputCursor {
		return buffered != ""
	}
	return baseline.buffered != buffered || baseline.cursor != cursor
}

func screenInputLines(screen Screen) ([]string, int, bool) {
	if screen.Text == "" {
		return nil, 0, false
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
	return lines, row, true
}

// inputBuffer joins lines[start:end] into one input buffer with leading
// blanks and the prompt removed. The cursor column is in terminal cells, so
// it is converted to a rune offset on its row before it is mapped into the
// buffer. With restoreBlanks, blanks between the text and a cursor past it
// are kept as typed input; otherwise the cursor is clamped to the text.
func inputBuffer(lines []string, start, end, row, cursorCol int, promptPrefix func(string) int, restoreBlanks bool) (string, int) {
	parts := append([]string(nil), lines[start:end]...)
	if len(parts) == 0 {
		return "", 0
	}
	rawCursor := 0
	for index := start; index < row; index++ {
		rawCursor += len([]rune(lines[index]))
	}
	rawCursor += runeIndexAtCell(lines[row], cursorCol)
	before := len([]rune(parts[0]))
	parts[0] = strings.TrimLeft(parts[0], " \t")
	removed := before - len([]rune(parts[0]))
	if prompt := promptPrefix(parts[0]); prompt > 0 {
		removed += prompt
		if runes := []rune(parts[0]); prompt < len(runes) {
			parts[0] = string(runes[prompt:])
		} else {
			parts[0] = ""
		}
	}
	buffered := strings.Join(parts, "")
	length := len([]rune(buffered))
	cursor := rawCursor - removed
	if cursor < 0 {
		cursor = 0
	}
	if cursor > length {
		if restoreBlanks {
			// The rendered screen drops trailing blanks; a cursor past the
			// text means typed spaces before it.
			buffered += strings.Repeat(" ", cursor-length)
		} else {
			cursor = length
		}
	}
	return buffered, cursor
}

// runeIndexAtCell converts a cell column on a rendered row into a rune
// offset; columns past the rendered text count as trailing blanks.
func runeIndexAtCell(line string, col int) int {
	width := 0
	index := 0
	for _, r := range line {
		if width >= col {
			return index
		}
		width += terminal.RuneWidth(r)
		index++
	}
	if col > width {
		index += col - width
	}
	return index
}

func softWrapped(line string, cols int) bool {
	return cols > 0 && terminal.StringWidth(line) >= cols
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

// inputPromptPrefix recognizes the prompt of the shell input line for the
// pending-input check. The rendered screen drops trailing blanks, so an empty
// prompt such as "user@host:~$ " arrives as "user@host:~$"; it still counts,
// and its length includes the dropped blank.
func inputPromptPrefix(line string) int {
	line = strings.TrimLeft(line, " \t")
	runes := []rune(line)
	for index, r := range runes {
		if r != '$' && r != '#' && r != '%' {
			continue
		}
		spaced := index+1 < len(runes) && runes[index+1] == ' '
		if !spaced && index != len(runes)-1 {
			continue
		}
		// A ':' alone does not mark a "%" prompt: "Progress: 50%" is output.
		prefix := string(runes[:index])
		if index == 0 || strings.Contains(prefix, "@") || (r != '%' && strings.Contains(prefix, ":")) {
			return index + 2
		}
	}
	return 0
}
