package terminal

// ModeTracker follows DEC private modes on the decoded output stream:
// application cursor keys (?1), cursor visibility (?25) and bracketed
// paste (?2004). The VT library does not export these, so they are tracked
// here with a small stream parser. Sequences split across chunks are
// handled by the persistent state machine, not by tail buffers.
//
// OSC/DCS/SOS/PM/APC strings are skipped so mode-looking bytes inside
// them never trigger false transitions. RIS (ESC c) resets the modes.
//
// ModeTracker is stateful and not safe for concurrent use.
type ModeTracker struct {
	state         byte
	private       bool
	intermediate  bool
	params        []byte
	cursorVisible bool
	appCursor     bool
	bracketed     bool
}

const (
	modeGround = iota
	modeEsc
	modeCSI
	modeOSC
	modeString    // DCS/SOS/PM/APC: ST-terminated
	modeStringEsc // ESC seen inside OSC/string; maybe ST
	modeOSCEsc
)

// NewModeTracker returns a tracker with power-on defaults: cursor visible,
// application cursor and bracketed paste off.
func NewModeTracker() *ModeTracker {
	return &ModeTracker{state: modeGround, cursorVisible: true}
}

// Reset restores power-on defaults (RIS).
func (m *ModeTracker) Reset() {
	m.state = modeGround
	m.params = m.params[:0]
	m.private, m.intermediate = false, false
	m.cursorVisible = true
	m.appCursor = false
	m.bracketed = false
}

// CursorVisible reports DECSET ?25 state.
func (m *ModeTracker) CursorVisible() bool { return m.cursorVisible }

// ApplicationCursor reports DECSET ?1 (DECCKM) state.
func (m *ModeTracker) ApplicationCursor() bool { return m.appCursor }

// BracketedPaste reports DECSET ?2004 state.
func (m *ModeTracker) BracketedPaste() bool { return m.bracketed }

// Feed consumes decoded terminal output.
func (m *ModeTracker) Feed(p []byte) {
	for _, b := range p {
		m.advance(b)
	}
}

func (m *ModeTracker) advance(b byte) {
	switch m.state {
	case modeGround:
		if b == 0x1b {
			m.state = modeEsc
		}
	case modeEsc:
		switch b {
		case '[':
			m.state = modeCSI
			m.params = m.params[:0]
			m.private, m.intermediate = false, false
		case ']':
			m.state = modeOSC
		case 'P', 'X', '^', '_':
			m.state = modeString
		case 'c': // RIS: full reset
			m.Reset()
		case 0x1b:
			// Stay in escape state.
		default:
			// Two-byte escape (ESC 7, ESC 8, ESC M, charset
			// selection with one final byte, ...). Charset
			// designation ESC ( X has an extra byte; treating X
			// as ground text does not affect mode tracking.
			m.state = modeGround
		}
	case modeCSI:
		switch {
		case b == 0x1b:
			m.state = modeEsc
		case b == 0x18 || b == 0x1a: // CAN/SUB abort
			m.state = modeGround
		case b >= 0x30 && b <= 0x3f: // parameter bytes
			if !m.intermediate && len(m.params) < 128 {
				if b == '?' && len(m.params) == 0 {
					m.private = true
				}
				m.params = append(m.params, b)
			}
		case b >= 0x20 && b <= 0x2f: // intermediate bytes
			m.intermediate = true
		case b >= 0x40 && b <= 0x7e: // final byte
			m.dispatch(b)
			m.state = modeGround
		default:
			// C0 controls inside CSI are executed and ignored here.
		}
	case modeOSC:
		switch b {
		case 0x07: // BEL terminates OSC
			m.state = modeGround
		case 0x1b:
			m.state = modeOSCEsc
		case 0x18, 0x1a:
			m.state = modeGround
		}
	case modeOSCEsc, modeStringEsc:
		switch b {
		case '\\': // ST
			m.state = modeGround
		case 0x1b:
			// Stay: another ESC, still waiting for ST.
		default:
			m.state = modeGround
		}
	case modeString:
		switch b {
		case 0x1b:
			m.state = modeStringEsc
		case 0x18, 0x1a:
			m.state = modeGround
		}
	}
}

func (m *ModeTracker) dispatch(final byte) {
	if final != 'h' && final != 'l' {
		return
	}
	if !m.private || m.intermediate {
		return
	}
	set := final == 'h'
	params := m.params
	if len(params) > 0 && params[0] == '?' {
		params = params[1:]
	}
	for _, n := range parseParams(params) {
		switch n {
		case 1:
			m.appCursor = set
		case 25:
			m.cursorVisible = set
		case 2004:
			m.bracketed = set
		}
	}
}

// parseParams splits semicolon-separated CSI parameters. Empty parameters
// are skipped (they mean "default", which for these modes is 0 and does
// not match any tracked mode).
func parseParams(p []byte) []int {
	var out []int
	n, has := 0, false
	for _, b := range p {
		switch {
		case b >= '0' && b <= '9':
			n = n*10 + int(b-'0')
			has = true
		case b == ';':
			if has {
				out = append(out, n)
			}
			n, has = 0, false
		default:
			// Non-numeric parameter bytes (":", ...): stop.
			return out
		}
	}
	if has {
		out = append(out, n)
	}
	return out
}
