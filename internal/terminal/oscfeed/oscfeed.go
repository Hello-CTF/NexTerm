// Package oscfeed feeds terminal output to an x/ansi parser and collects OSC strings itself.
//
// The parser keeps the bytes of an OSC string raw, so it reads 0x9C, the 8-bit string terminator, as the end of the
// string even when it is a UTF-8 continuation byte: the title "看" (E7 9C 8B) ends after its first byte, and the rest
// of the title is printed as screen text. xterm.js decodes UTF-8 before it parses, so the browser terminal keeps the
// whole title. The feeder ends an OSC string at BEL, ESC \ or a 0x9C that does not continue a UTF-8 character, which
// keeps the emulators in step with the terminal the user sees.
package oscfeed

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

const (
	bel = 0x07
	can = 0x18
	sub = 0x1a
	esc = 0x1b
	st  = 0x9c
)

// Feeder owns the OSC strings of one parser; the parser handles every other byte.
type Feeder struct {
	parser *ansi.Parser
	handle func(cmd int, data []byte)
	limit  int

	inOSC bool
	// escape is set after an ESC inside an OSC string, which ends the string; a following '\' completes the ST.
	escape bool
	// pending counts the continuation bytes the current UTF-8 character still needs.
	pending int
	data    []byte
}

// New returns a feeder for p. handle receives each OSC string as x/ansi would pass it (the command number, or
// parser.MissingCommand, and the data with its "N;" prefix); data past limit bytes is dropped, as the parser's
// bounded buffer does. The parser's own OSC handler is not called.
func New(p *ansi.Parser, limit int, handle func(cmd int, data []byte)) *Feeder {
	return &Feeder{parser: p, handle: handle, limit: limit}
}

// Write feeds p.
func (f *Feeder) Write(p []byte) {
	for _, b := range p {
		f.Advance(b)
	}
}

// Advance feeds one byte.
func (f *Feeder) Advance(b byte) {
	if !f.inOSC {
		f.parser.Advance(b)
		if f.parser.State() == parser.OscStringState {
			// Take the string over: CAN returns the parser to the ground state without dispatching anything.
			f.parser.Advance(can)
			f.inOSC, f.escape, f.pending, f.data = true, false, 0, f.data[:0]
		}
		return
	}
	if f.escape {
		f.finish()
		if b == '\\' {
			return
		}
		f.parser.Advance(esc)
		f.Advance(b)
		return
	}
	switch {
	case b == esc:
		f.escape = true
	case b == bel, b == st && f.pending == 0:
		f.finish()
	case b == can, b == sub:
		f.inOSC = false
	case b < 0x20:
		// Other C0 controls are ignored inside the string, as the parser does.
	default:
		f.collect(b)
	}
}

func (f *Feeder) collect(b byte) {
	switch {
	case f.pending > 0 && b >= 0x80 && b <= 0xbf:
		f.pending--
	case b >= 0xc2 && b <= 0xdf:
		f.pending = 1
	case b >= 0xe0 && b <= 0xef:
		f.pending = 2
	case b >= 0xf0 && b <= 0xf4:
		f.pending = 3
	default:
		f.pending = 0
	}
	if len(f.data) < f.limit {
		f.data = append(f.data, b)
	}
}

func (f *Feeder) finish() {
	f.inOSC, f.escape = false, false
	if f.handle != nil {
		f.handle(command(f.data), f.data)
	}
}

// command reads the leading decimal digits the way the parser does.
func command(data []byte) int {
	cmd := parser.MissingCommand
	for _, d := range data {
		if d < '0' || d > '9' {
			break
		}
		if cmd == parser.MissingCommand {
			cmd = 0
		}
		cmd = cmd*10 + int(d-'0')
	}
	return cmd
}
