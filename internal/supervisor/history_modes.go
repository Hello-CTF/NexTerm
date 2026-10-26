package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// terminalModes is the terminal state a replay that starts mid-stream cannot
// reconstruct from the replayed bytes and a full-screen program does not
// resend on redraw: the screen buffer, the input modes, the scroll region,
// origin mode and the G0/G1 character sets. SGR attributes are left out; a
// program resets them on its next styled write.
//
// The helper feeds every recorded byte to a live tracker and stores a
// snapshot at the start of each segment, so the modes at any retained offset
// are the snapshot of its segment plus at most one segment of parsing, and
// modes set long before the history base survive eviction. The parser state
// is part of the snapshot because a segment can start inside a sequence.
type terminalModes struct {
	AltScreen    bool   `json:"alt_screen,omitempty"`
	AppCursor    bool   `json:"app_cursor,omitempty"`
	AppKeypad    bool   `json:"app_keypad,omitempty"`
	Bracketed    bool   `json:"bracketed,omitempty"`
	CursorHidden bool   `json:"cursor_hidden,omitempty"`
	NoAutowrap   bool   `json:"no_autowrap,omitempty"`
	Origin       bool   `json:"origin,omitempty"`
	Mouse        uint16 `json:"mouse,omitempty"`
	G0Special    bool   `json:"g0_special,omitempty"`
	G1Special    bool   `json:"g1_special,omitempty"`
	ShiftOut     bool   `json:"shift_out,omitempty"`
	ScrollTop    int    `json:"scroll_top,omitempty"`
	ScrollBottom int    `json:"scroll_bottom,omitempty"`

	State        byte     `json:"state,omitempty"`
	Private      bool     `json:"private,omitempty"`
	Intermediate byte     `json:"intermediate,omitempty"`
	Params       [32]byte `json:"params"`
	ParamLen     uint8    `json:"param_len,omitempty"`
}

var mouseModes = []int{9, 1000, 1001, 1002, 1003, 1004, 1005, 1006, 1015, 1016}

func (m *terminalModes) feed(data []byte) {
	for _, b := range data {
		m.advance(b)
	}
}

func (m *terminalModes) resetState() {
	m.State, m.Private, m.Intermediate, m.ParamLen = scanGround, false, 0, 0
}

func (m *terminalModes) control(b byte) bool {
	switch b {
	case 0x0e:
		m.ShiftOut = true
	case 0x0f:
		m.ShiftOut = false
	default:
		return false
	}
	return true
}

func (m *terminalModes) advance(b byte) {
	switch m.State {
	case scanGround:
		if b == 0x1b {
			m.State, m.Intermediate = scanEsc, 0
			return
		}
		m.control(b)
	case scanEsc:
		switch {
		case b == '[' && m.Intermediate == 0:
			m.State, m.Private, m.ParamLen = scanCSI, false, 0
		case (b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_') && m.Intermediate == 0:
			m.State = scanString
		case b == 0x1b:
			m.Intermediate = 0
		case b >= 0x20 && b <= 0x2f:
			m.Intermediate = b
		case b < 0x20:
			m.control(b)
		default:
			m.escFinal(b)
			m.State = scanGround
		}
	case scanCSI:
		switch {
		case b == 0x1b:
			m.State, m.Intermediate = scanEsc, 0
		case b == 0x18 || b == 0x1a:
			m.resetState()
		case b < 0x20:
			m.control(b)
		case b == '?' && m.ParamLen == 0:
			m.Private = true
		case b >= 0x40 && b <= 0x7e:
			m.csiFinal(b)
			m.resetState()
		case b >= 0x20 && b <= 0x2f:
			m.Intermediate = b
		default:
			if int(m.ParamLen) < len(m.Params) {
				m.Params[m.ParamLen] = b
				m.ParamLen++
			}
		}
	case scanString:
		switch b {
		case 0x1b:
			m.State = scanStringEsc
		case 0x07, 0x18, 0x1a:
			m.State = scanGround
		}
	case scanStringEsc:
		if b == '\\' {
			m.State = scanGround
		} else {
			m.State = scanString
		}
	}
}

func (m *terminalModes) escFinal(b byte) {
	switch m.Intermediate {
	case 0:
		switch b {
		case '=':
			m.AppKeypad = true
		case '>':
			m.AppKeypad = false
		case 'c':
			*m = terminalModes{}
		}
	case '(':
		m.G0Special = b == '0'
	case ')':
		m.G1Special = b == '0'
	}
}

func (m *terminalModes) params() []int {
	var values []int
	current, seen := 0, false
	for _, b := range m.Params[:m.ParamLen] {
		switch {
		case b >= '0' && b <= '9':
			current = min(current*10+int(b-'0'), 1<<16)
			seen = true
		case b == ';':
			if !seen {
				current = 0
			}
			values = append(values, current)
			current, seen = 0, false
		}
	}
	if seen || len(values) > 0 {
		values = append(values, current)
	}
	return values
}

func (m *terminalModes) csiFinal(b byte) {
	switch {
	case m.Private && (b == 'h' || b == 'l') && m.Intermediate == 0:
		for _, mode := range m.params() {
			m.setPrivate(mode, b == 'h')
		}
	case !m.Private && b == 'r' && m.Intermediate == 0:
		values := m.params()
		m.ScrollTop, m.ScrollBottom = 0, 0
		if len(values) > 0 {
			m.ScrollTop = values[0]
		}
		if len(values) > 1 {
			m.ScrollBottom = values[1]
		}
		if m.ScrollTop <= 1 && m.ScrollBottom == 0 {
			m.ScrollTop = 0
		}
	case !m.Private && b == 'p' && m.Intermediate == '!':
		// DECSTR soft reset.
		m.AppCursor, m.AppKeypad, m.CursorHidden, m.Origin = false, false, false, false
		m.ScrollTop, m.ScrollBottom = 0, 0
		m.G0Special, m.G1Special, m.ShiftOut = false, false, false
	}
}

func (m *terminalModes) setPrivate(mode int, on bool) {
	switch mode {
	case 1:
		m.AppCursor = on
	case 6:
		m.Origin = on
	case 7:
		m.NoAutowrap = !on
	case 25:
		m.CursorHidden = !on
	case 47, 1047, 1049:
		m.AltScreen = on
	case 2004:
		m.Bracketed = on
	default:
		for index, mouse := range mouseModes {
			if mode == mouse {
				if on {
					m.Mouse |= 1 << index
				} else {
					m.Mouse &^= 1 << index
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
	if m.AltScreen {
		set(1049)
	}
	if m.ScrollTop > 0 || m.ScrollBottom > 0 {
		out = append(out, "\x1b["...)
		if m.ScrollTop > 0 {
			out = strconv.AppendInt(out, int64(m.ScrollTop), 10)
		}
		out = append(out, ';')
		if m.ScrollBottom > 0 {
			out = strconv.AppendInt(out, int64(m.ScrollBottom), 10)
		}
		out = append(out, 'r')
	}
	if m.Origin {
		set(6)
	}
	if m.AppCursor {
		set(1)
	}
	if m.AppKeypad {
		out = append(out, "\x1b="...)
	}
	if m.Bracketed {
		set(2004)
	}
	for index, mode := range mouseModes {
		if m.Mouse&(1<<index) != 0 {
			set(mode)
		}
	}
	if m.NoAutowrap {
		out = append(out, "\x1b[?7l"...)
	}
	if m.G0Special {
		out = append(out, "\x1b(0"...)
	}
	if m.G1Special {
		out = append(out, "\x1b)0"...)
	}
	if m.ShiftOut {
		out = append(out, 0x0e)
	}
	if m.CursorHidden {
		out = append(out, "\x1b[?25l"...)
	}
	return out
}

const historyModesPrefix = "modes-"

func historyModesPath(dir string, start uint64) string {
	return filepath.Join(dir, fmt.Sprintf("%s%0*d.json", historyModesPrefix, historyOffsetDigits, start))
}

// writeModesSnapshot persists the modes at the start of a segment. It is
// written before the segment is created, so a segment never lacks one
// because of a crash; an orphan snapshot without its segment is ignored.
func writeModesSnapshot(dir string, start uint64, modes terminalModes) error {
	data, err := json.Marshal(modes)
	if err != nil {
		return err
	}
	path := historyModesPath(dir, start)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// readModesSnapshot returns the persisted modes at a segment start; nil when
// there is none (a segment at offset zero starts in the default state).
func readModesSnapshot(dir string, start uint64) *terminalModes {
	if start == 0 {
		return &terminalModes{}
	}
	data, err := os.ReadFile(historyModesPath(dir, start))
	if err != nil {
		return nil
	}
	var modes terminalModes
	if json.Unmarshal(data, &modes) != nil || int(modes.ParamLen) > len(modes.Params) {
		return nil
	}
	return &modes
}

// modesPreludeAt returns the prelude for the modes in effect at offset: the
// snapshot of the segment holding it, advanced over at most one segment.
func (h *history) modesPreludeAt(offset uint64) ([]byte, error) {
	modes, _, err := h.modesAt(offset)
	if err != nil {
		return nil, err
	}
	return modes.prelude(), nil
}

// modesAt returns the modes in effect at offset and how many recorded bytes
// it parsed to get there (at most one segment).
func (h *history) modesAt(offset uint64) (terminalModes, uint64, error) {
	for attempt := 0; attempt < 4; attempt++ {
		h.mu.Lock()
		if offset < h.baseLocked() || offset > h.next || len(h.segments) == 0 {
			h.mu.Unlock()
			return terminalModes{}, 0, errHistoryEvicted
		}
		index := len(h.segments) - 1
		for index > 0 && h.segments[index].start > offset {
			index--
		}
		segment := h.segments[index]
		var modes terminalModes
		if segment.modes != nil {
			modes = *segment.modes
		}
		h.mu.Unlock()

		reader := h.newReader(segment.start)
		buffer := make([]byte, 128<<10)
		var err error
		for reader.pos < offset {
			var count int
			count, err = reader.read(buffer[:min(uint64(len(buffer)), offset-reader.pos)])
			if err != nil || count == 0 {
				break
			}
			modes.feed(buffer[:count])
		}
		reader.close()
		if errors.Is(err, errHistoryEvicted) {
			continue
		}
		if err != nil {
			return terminalModes{}, 0, err
		}
		if reader.pos < offset {
			return terminalModes{}, 0, fmt.Errorf("%w: recorded output ends before %d", ErrUnavailable, offset)
		}
		return modes, offset - segment.start, nil
	}
	return terminalModes{}, 0, errHistoryEvicted
}
