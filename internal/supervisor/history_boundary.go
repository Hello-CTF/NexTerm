package supervisor

import (
	"bytes"
	"strconv"
)

// Escape parser states shared by the boundary scan and the mode scan.
const (
	scanGround byte = iota
	scanEsc
	scanCSI
	scanString
	scanStringEsc
)

// safeReplayStart returns the index in data where a replay that must not
// begin mid-sequence can start. data begins at an arbitrary offset, so the
// parser state there is unknown: a string terminator (ESC \) seen outside a
// string proves the scan began inside an OSC/DCS string and discards every
// candidate before it. The first line start outside any escape sequence wins;
// without one, the first character boundary outside any escape sequence is
// used; without either, len(data).
func safeReplayStart(data []byte) int {
	state := scanGround
	line, char := -1, -1
	for index, b := range data {
		if state == scanGround && char < 0 && (b < 0x80 || b >= 0xc0) && b != 0x1b {
			char = index
		}
		switch state {
		case scanGround:
			switch b {
			case 0x1b:
				state = scanEsc
			case '\n':
				if line < 0 {
					line = index + 1
				}
			}
		case scanEsc:
			switch {
			case b == '\\':
				// A lone string terminator: the scan started inside a string.
				line, char = -1, -1
				state = scanGround
			case b == '[':
				state = scanCSI
			case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
				state = scanString
			case b == 0x1b:
			case b >= 0x20 && b <= 0x2f:
			default:
				state = scanGround
			}
		case scanCSI:
			switch {
			case b == 0x1b:
				state = scanEsc
			case b == 0x18 || b == 0x1a:
				state = scanGround
			case b >= 0x40 && b <= 0x7e:
				state = scanGround
			}
		case scanString:
			switch b {
			case 0x1b:
				state = scanStringEsc
			case 0x07, 0x18, 0x1a:
				state = scanGround
			}
		case scanStringEsc:
			if b == '\\' {
				state = scanGround
			} else {
				state = scanString
			}
		}
	}
	if line >= 0 {
		return line
	}
	if char >= 0 {
		return char
	}
	return len(data)
}

// terminalModes is the subset of terminal state a replay that starts mid-
// stream cannot reconstruct and a full-screen program does not resend on
// redraw: the screen buffer and the input modes.
type terminalModes struct {
	altScreen    bool
	appCursor    bool
	appKeypad    bool
	bracketed    bool
	cursorHidden bool
	noAutowrap   bool
	mouse        map[int]bool

	state   byte
	private bool
	params  []byte
}

var mouseModes = []int{9, 1000, 1001, 1002, 1003, 1004, 1005, 1006, 1015, 1016}

func (m *terminalModes) feed(data []byte) {
	for _, b := range data {
		m.advance(b)
	}
}

func (m *terminalModes) advance(b byte) {
	switch m.state {
	case scanGround:
		if b == 0x1b {
			m.state = scanEsc
		}
	case scanEsc:
		switch {
		case b == '[':
			m.state = scanCSI
			m.private = false
			m.params = m.params[:0]
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			m.state = scanString
		case b == '=':
			m.appKeypad = true
			m.state = scanGround
		case b == '>':
			m.appKeypad = false
			m.state = scanGround
		case b == 'c':
			*m = terminalModes{mouse: m.mouse, params: m.params[:0]}
			clear(m.mouse)
		case b == 0x1b:
		case b >= 0x20 && b <= 0x2f:
		default:
			m.state = scanGround
		}
	case scanCSI:
		switch {
		case b == 0x1b:
			m.state = scanEsc
		case b == 0x18 || b == 0x1a:
			m.state = scanGround
		case b == '?' && len(m.params) == 0:
			m.private = true
		case b >= 0x40 && b <= 0x7e:
			if m.private && (b == 'h' || b == 'l') {
				m.setPrivate(b == 'h')
			}
			m.state = scanGround
		default:
			if len(m.params) < 64 {
				m.params = append(m.params, b)
			}
		}
	case scanString:
		switch b {
		case 0x1b:
			m.state = scanStringEsc
		case 0x07, 0x18, 0x1a:
			m.state = scanGround
		}
	case scanStringEsc:
		if b == '\\' {
			m.state = scanGround
		} else {
			m.state = scanString
		}
	}
}

func (m *terminalModes) setPrivate(on bool) {
	for _, field := range bytes.Split(m.params, []byte{';'}) {
		mode, err := strconv.Atoi(string(field))
		if err != nil {
			continue
		}
		switch mode {
		case 1:
			m.appCursor = on
		case 7:
			m.noAutowrap = !on
		case 25:
			m.cursorHidden = !on
		case 47, 1047, 1049:
			m.altScreen = on
		case 2004:
			m.bracketed = on
		default:
			for _, mouse := range mouseModes {
				if mode == mouse {
					if m.mouse == nil {
						m.mouse = make(map[int]bool)
					}
					m.mouse[mode] = on
				}
			}
		}
	}
}

// prelude returns the sequence that puts a fresh terminal into these modes.
// It is empty for a terminal in its default state.
func (m *terminalModes) prelude() []byte {
	var out []byte
	set := func(mode int) {
		out = append(out, "\x1b[?"...)
		out = strconv.AppendInt(out, int64(mode), 10)
		out = append(out, 'h')
	}
	if m.altScreen {
		set(1049)
	}
	if m.appCursor {
		set(1)
	}
	if m.appKeypad {
		out = append(out, "\x1b="...)
	}
	if m.bracketed {
		set(2004)
	}
	for _, mode := range mouseModes {
		if m.mouse[mode] {
			set(mode)
		}
	}
	if m.noAutowrap {
		out = append(out, "\x1b[?7l"...)
	}
	if m.cursorHidden {
		out = append(out, "\x1b[?25l"...)
	}
	return out
}

// modesPreludeAt scans the retained output before offset and returns the
// prelude for the modes in effect there. Modes set before the history base
// are unknown and treated as defaults.
func (h *history) modesPreludeAt(offset uint64) ([]byte, error) {
	base, _ := h.bounds()
	if offset <= base {
		return nil, nil
	}
	reader := h.newReader(base)
	defer reader.close()
	var modes terminalModes
	buffer := make([]byte, 128<<10)
	for reader.pos < offset {
		count, err := reader.read(buffer[:min(uint64(len(buffer)), offset-reader.pos)])
		if err == errHistoryEvicted {
			// The scan fell behind eviction: restart from the new base.
			modes = terminalModes{}
			base, _ = h.bounds()
			if base >= offset {
				return nil, nil
			}
			reader.pos = base
			continue
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			break
		}
		modes.feed(buffer[:count])
	}
	return modes.prelude(), nil
}
