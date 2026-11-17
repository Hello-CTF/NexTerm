package tools

import (
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// InputBoundary says how the start of the shell input line was found.
type InputBoundary uint8

const (
	// InputBoundaryNone means the screen had no text to read.
	InputBoundaryNone InputBoundary = iota
	// InputBoundaryMarked means the shell marked where its prompt ends
	// (OSC 133;B), so the input starts exactly there.
	InputBoundaryMarked
	// InputBoundaryPrompt means a shell prompt was recognized on screen and
	// the input starts after it.
	InputBoundaryPrompt
	// InputBoundaryUnknown means no prompt was recognized: the text is every
	// row that could hold pending input, prompt decoration included.
	InputBoundaryUnknown
)

// TerminalInput is the shell input line read from a screen: the text that
// Enter would submit together with new keys, and the cursor as a rune
// offset into it.
type TerminalInput struct {
	Text     string
	Cursor   int
	Boundary InputBoundary
	// Line is, for an unknown boundary, the text of the cursor's row (with
	// the rows it soft-wraps from) and every row below it: whatever the
	// shell could still be holding as input.
	Line string
}

// UnknownInputBoundaryReason explains why keys that submit are confirmed when
// no prompt was recognized on a row that already shows text.
const UnknownInputBoundaryReason = "未识别出终端提示符，无法确认当前行已有的文字是否会随回车一起执行；本次发送的按键已单独检查"

// ClassifyTerminalKeys classifies send_keys input against the terminal input
// line it will join. With a known input boundary the keys are applied to the
// pending input and the whole submission is classified. Without one the
// keys are classified on their own, never prompt decoration; when they
// submit and the cursor's line already shows text, the user confirms,
// because that text may be submitted with them.
func ClassifyTerminalKeys(keys string, enter bool, input TerminalInput, rules []string) guard.Ruling {
	if input.Boundary != InputBoundaryUnknown {
		return guard.ClassifySendKeysWithCursor(keys, input.Text, input.Cursor, enter, rules)
	}
	ruling := guard.ClassifySendKeysWithCursor(keys, "", 0, enter, rules)
	if _, _, submits := guard.EffectiveKeyActions(keys, "", 0, enter); submits && strings.TrimSpace(input.Line) != "" {
		ruling = guard.Worst(ruling, guard.Indeterminate(UnknownInputBoundaryReason))
	}
	return ruling
}

// DisplayInput returns the pending input the confirmation card shows the
// keys joined with: none when the boundary is unknown, since the visible
// text cannot be told apart from the prompt.
func (in TerminalInput) DisplayInput() (string, int) {
	if in.Boundary == InputBoundaryUnknown {
		return "", 0
	}
	return in.Text, in.Cursor
}

// TerminalInputCursor returns the input buffer the guard classifies against
// and the cursor as a rune offset into it; see ReadTerminalInput. When no
// prompt is recognized the buffer over-approximates (prompt included), so
// text already on screen is never left out of a submission check.
func TerminalInputCursor(screen Screen) (string, int) {
	input := ReadTerminalInput(screen)
	return input.Text, input.Cursor
}

// ReadTerminalInput locates the shell input line on screen. The input starts
// at the shell's OSC 133;B mark when it sent one, otherwise after the last
// recognized prompt at or above the cursor; it never reaches above the
// OSC 133;A prompt-start mark. Rows below the cursor are included, so a
// multi-row buffer is classified whole. A right-aligned prompt segment on the
// input row is dropped.
func ReadTerminalInput(screen Screen) TerminalInput {
	lines, row, ok := screenInputLines(screen)
	if !ok {
		return TerminalInput{}
	}
	if mark := screen.InputStart; mark.Set && !screen.AltScreen && mark.Row >= 0 && mark.Row <= row {
		cut := func(line string) int { return runeIndexAtCell(line, mark.Col) }
		text, cursor := inputBuffer(lines, mark.Row, len(lines), row, screen.CursorCol, cut, false, screen.Cols)
		return TerminalInput{Text: text, Cursor: cursor, Boundary: InputBoundaryMarked}
	}
	floor := 0
	if mark := screen.PromptStart; mark.Set && !screen.AltScreen && mark.Row >= 0 && mark.Row <= row {
		floor = mark.Row
	}
	cut := func(line string) int { return leadingPrompt(line, false) }
	for index := row; index >= floor; index-- {
		if cut(lines[index]) > 0 {
			text, cursor := inputBuffer(lines, index, len(lines), row, screen.CursorCol, cut, false, screen.Cols)
			return TerminalInput{Text: text, Cursor: cursor, Boundary: InputBoundaryPrompt}
		}
	}
	text, cursor := inputBuffer(lines, floor, len(lines), row, screen.CursorCol, leadingBlanks, false, 0)
	lineStart := row
	for lineStart > floor && softWrapped(lines[lineStart-1], screen.Cols) {
		lineStart--
	}
	return TerminalInput{Text: text, Cursor: cursor, Boundary: InputBoundaryUnknown, Line: strings.Join(lines[lineStart:], "\n")}
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
	cut := func(line string) int { return leadingPrompt(line, true) }
	start := -1
	for index := row; index >= 0; index-- {
		if cut(lines[index]) > 0 {
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
	buffered, cursor := inputBuffer(lines, start, end, row, screen.CursorCol, cut, true, 0)
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

// inputBuffer joins lines[start:end] into one input buffer with the leading
// cut runes of the first line (blanks and the prompt) removed; cut may count
// blanks the rendered row dropped after the prompt. The cursor column is in
// terminal cells, so it is converted to a rune offset on its row before it is
// mapped into the buffer. With restoreBlanks, blanks between the text and a
// cursor past it are kept as typed input; otherwise the cursor is clamped to
// the text. A positive cols enables dropping a right prompt from the first
// row.
func inputBuffer(lines []string, start, end, row, cursorCol int, cut func(string) int, restoreBlanks bool, cols int) (string, int) {
	parts := append([]string(nil), lines[start:end]...)
	if len(parts) == 0 {
		return "", 0
	}
	rawCursor := 0
	for index := start; index < row; index++ {
		rawCursor += len([]rune(lines[index]))
	}
	rawCursor += runeIndexAtCell(lines[row], cursorCol)
	removed := 0
	if prefix := cut(parts[0]); prefix > 0 {
		removed = prefix
		if runes := []rune(parts[0]); prefix < len(runes) {
			parts[0] = string(runes[prefix:])
		} else {
			parts[0] = ""
		}
	}
	if cols > 0 {
		cursorOnFirst := -1
		if row == start {
			cursorOnFirst = rawCursor - removed
		}
		parts[0] = dropRightPrompt(parts[0], lines[start], cursorOnFirst, cols)
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

// rightPromptGap is the run of blanks that separates a right prompt (zsh
// RPROMPT, fish_right_prompt) from the input on the same row.
const rightPromptGap = "   "

// dropRightPrompt removes a right-aligned prompt segment from the input text
// of the first input row. The row must end one or two cells before the right
// edge (where shells put a right prompt, and where a soft-wrapped input row
// never ends), and the segment must start after the cursor, follow a wide
// blank gap, be short and contain no shell syntax, so typed input is never
// mistaken for it.
func dropRightPrompt(text, line string, cursor, cols int) string {
	width := terminal.StringWidth(line)
	if width >= cols || width < cols-2 {
		return text
	}
	gap := strings.LastIndex(text, rightPromptGap)
	if gap < 0 {
		return text
	}
	segment := strings.TrimLeft(text[gap:], " ")
	if segment == "" || strings.ContainsAny(segment, ";&|<>`$(){}\\'\"\t*?!=") {
		return text
	}
	if terminal.StringWidth(segment) > cols/3 {
		return text
	}
	if cursor >= 0 && len([]rune(text[:gap])) < cursor {
		return text
	}
	return strings.TrimRight(text[:gap], " ")
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

func leadingBlanks(line string) int {
	return len([]rune(line)) - len([]rune(strings.TrimLeft(line, " \t")))
}

// leadingPrompt returns how many runes at the start of line are blanks and
// a recognized shell prompt, or 0 when line does not start with a prompt.
// The rendered screen drops trailing blanks, so an empty prompt such as
// "user@host:~$ " arrives as "user@host:~$"; it still counts, and the length
// includes the dropped blank.
func leadingPrompt(line string, strict bool) int {
	if prompt := promptPrefix(strings.TrimLeft(line, " \t"), strict); prompt > 0 {
		return leadingBlanks(line) + prompt
	}
	return 0
}

// promptPrefix recognizes common shell prompts at the start of a line with
// leading blanks removed: a prompt character ($ # % ❯, or PowerShell's >)
// followed by a blank or the end of the line, preceded by text shaped like a
// prompt — user@host, a path after ':', one [..] or (..) group, "bash-5.2",
// box-drawing decoration such as the "└─$" second row of a two-line Kali
// prompt, or nothing. The first match wins, so a prompt character inside
// typed input after the prompt is ignored. strict rejects a ':' before '%',
// so output such as "Progress: 50%" is not taken for a prompt.
func promptPrefix(line string, strict bool) int {
	runes := []rune(line)
	for index, r := range runes {
		if !promptChar(r) {
			continue
		}
		if index+1 < len(runes) && runes[index+1] != ' ' {
			continue
		}
		if promptShaped(string(runes[:index]), r, strict) {
			return index + 2
		}
	}
	return 0
}

func promptChar(r rune) bool {
	switch r {
	case '$', '#', '%', '>', '❯':
		return true
	}
	return false
}

func promptShaped(prefix string, r rune, strict bool) bool {
	if r == '>' {
		// Only PowerShell's "PS C:\path>"; a bare ">" is a continuation
		// prompt whose earlier rows belong to the same command.
		return strings.HasPrefix(prefix, "PS ") && len(prefix) > len("PS ")
	}
	trimmed := strings.TrimRight(prefix, " ")
	if trimmed == "" || boxDrawing(trimmed) {
		return true
	}
	if r == '❯' {
		return trimmed != prefix
	}
	if strings.Contains(prefix, "@") {
		return true
	}
	if strings.Contains(prefix, ":") && (r != '%' || !strict) {
		return true
	}
	return trimmed == prefix && (bracketGroup(prefix) || shellVersion(prefix))
}

// boxDrawing reports whether text is only box-drawing decoration.
func boxDrawing(text string) bool {
	for _, r := range text {
		if (r < 0x2500 || r > 0x257f) && r != ' ' {
			return false
		}
	}
	return true
}

// bracketGroup reports a prompt such as "[~]" or "(venv)": one bracketed
// group with no nested brackets.
func bracketGroup(text string) bool {
	if len(text) < 2 {
		return false
	}
	first, last := text[0], text[len(text)-1]
	if (first != '[' || last != ']') && (first != '(' || last != ')') {
		return false
	}
	return !strings.ContainsAny(text[1:len(text)-1], "[]()")
}

// shellVersion reports bash's default prompt name, such as "bash-5.2" or
// "sh-4.4".
func shellVersion(text string) bool {
	name, version, ok := strings.Cut(text, "-")
	if !ok || version == "" || !strings.HasSuffix(name, "sh") {
		return false
	}
	for _, r := range name {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	for _, r := range version {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}
