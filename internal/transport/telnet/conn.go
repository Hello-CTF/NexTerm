package telnet

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Telnet commands (RFC 854) and the options this client negotiates.
const (
	cmdSE   byte = 240
	cmdSB   byte = 250
	cmdWILL byte = 251
	cmdWONT byte = 252
	cmdDO   byte = 253
	cmdDONT byte = 254
	cmdIAC  byte = 255

	optBinary byte = 0  // RFC 856
	optEcho   byte = 1  // RFC 857
	optSGA    byte = 3  // RFC 858
	optTTYPE  byte = 24 // RFC 1091
	optNAWS   byte = 31 // RFC 1073

	ttypeIS   byte = 0
	ttypeSEND byte = 1

	// maxSubnegotiation bounds a subnegotiation body; longer ones are dropped.
	maxSubnegotiation = 4096
	// maxPending bounds decoded server output waiting for Read; the reader stops taking data from the connection
	// beyond it, so a stalled terminal pushes back on the server.
	maxPending = 256 << 10
)

// EchoMode chooses who shows typed characters.
type EchoMode string

const (
	// EchoAuto echoes locally while the server has not agreed to echo (RFC 857).
	EchoAuto EchoMode = "auto"
	// EchoOn always echoes locally, for servers that claim to echo but do not.
	EchoOn EchoMode = "on"
	// EchoOff never echoes locally.
	EchoOff EchoMode = "off"
)

func ParseEchoMode(value string) (EchoMode, error) {
	switch EchoMode(value) {
	case "", EchoAuto:
		return EchoAuto, nil
	case EchoOn, EchoOff:
		return EchoMode(value), nil
	}
	return "", errors.New("telnet local echo must be auto, on or off")
}

type connOptions struct {
	term string
	echo EchoMode
	// crlf sends Enter as CR LF instead of the NVT CR NUL.
	crlf bool
}

type echoState uint8

const (
	echoText echoState = iota
	echoEscape
	echoCSI
	echoSS3
)

var nextConnID atomic.Uint64

// optionState follows RFC 1143 closely enough to never answer an acknowledgment: a request is answered only when it
// changes the state, and a reply to our own request only settles it.
type optionState uint8

const (
	stateNo optionState = iota
	stateYes
	stateWantYes
)

// Options this side performs when asked (WILL) and options the server may perform (DO).
var (
	localSupported  = map[byte]bool{optBinary: true, optSGA: true, optTTYPE: true, optNAWS: true}
	remoteSupported = map[byte]bool{optBinary: true, optEcho: true, optSGA: true}
)

type parseState uint8

const (
	parseData parseState = iota
	parseIAC
	parseOption
	parseSB
	parseSBIAC
)

// Conn is one Telnet terminal. Read returns the server's data with the protocol removed, Write escapes the user's
// input, and Resize reports the window size through NAWS once the server accepted it.
type Conn struct {
	raw        net.Conn
	reader     *bufio.Reader
	options    connOptions
	id         string
	generation uint64
	onClose    func()

	// writeMu orders every write to the connection; Write also holds it while it queues the local echo, so echo
	// and input stay in the same order.
	writeMu   sync.Mutex
	echoParse echoState
	echoCR    bool

	mu         sync.Mutex
	local      [256]optionState
	remote     [256]optionState
	cols, rows uint16

	// Decoded output waiting for Read: server data and local echo.
	outMu   sync.Mutex
	outCond *sync.Cond
	out     []byte
	outErr  error

	// Parser state, owned by readLoop.
	parse   parseState
	command byte
	sb      []byte
	sawCR   bool

	closeOnce sync.Once
	done      chan struct{}
	readErr   atomic.Pointer[error]
}

func newConn(raw net.Conn, options connOptions, cols, rows uint16, generation uint64) *Conn {
	if options.echo == "" {
		options.echo = EchoAuto
	}
	conn := &Conn{
		raw: raw, reader: bufio.NewReaderSize(raw, 32<<10), options: options, cols: cols, rows: rows,
		id: "telnet-" + strconv.FormatUint(nextConnID.Add(1), 10), generation: generation, done: make(chan struct{}),
	}
	conn.outCond = sync.NewCond(&conn.outMu)
	return conn
}

// start sends the client's opening requests: it offers the window size and terminal type, and asks the server to
// echo and to suppress go-ahead, which puts it in character-at-a-time mode.
func (c *Conn) start(ctx context.Context) error {
	c.mu.Lock()
	var out []byte
	for _, option := range []byte{optNAWS, optTTYPE, optSGA} {
		c.local[option] = stateWantYes
		out = append(out, cmdIAC, cmdWILL, option)
	}
	for _, option := range []byte{optSGA, optEcho} {
		c.remote[option] = stateWantYes
		out = append(out, cmdIAC, cmdDO, option)
	}
	c.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.raw.SetWriteDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.raw.SetWriteDeadline(time.Unix(1, 0)) })
	err := c.writeRaw(out)
	// When the cancel callback already ran or is running, it may set the past deadline after any reset here, so
	// the connection is unusable: report the cancellation instead of handing it out.
	if !stop() {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if err := c.raw.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}
	go c.readLoop()
	return nil
}

func (c *Conn) writeRaw(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.raw.Write(data)
	return err
}

// Read returns server data with Telnet commands removed, followed in order by any local echo. It blocks until data
// is queued or the connection has ended and everything queued was read.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.outMu.Lock()
	defer c.outMu.Unlock()
	for len(c.out) == 0 && c.outErr == nil {
		c.outCond.Wait()
	}
	if len(c.out) == 0 {
		return 0, c.outErr
	}
	n := copy(p, c.out)
	c.out = c.out[n:]
	if len(c.out) == 0 {
		c.out = nil
	}
	c.outCond.Broadcast()
	return n, nil
}

// readLoop decodes the connection and answers negotiation as it arrives, so the server is answered even while the
// terminal is not reading.
func (c *Conn) readLoop() {
	buf := make([]byte, 32<<10)
	for {
		n, reply, err := c.decode(buf)
		if len(reply) > 0 {
			if writeErr := c.writeRaw(reply); writeErr != nil && err == nil {
				err = writeErr
			}
		}
		if n > 0 {
			c.queue(buf[:n], true)
		}
		if err != nil {
			c.finish(err)
			return
		}
	}
}

// queue appends output for Read. Server data waits while too much is pending; local echo never waits.
func (c *Conn) queue(data []byte, wait bool) {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	for wait && len(c.out) >= maxPending && c.outErr == nil {
		c.outCond.Wait()
	}
	if c.outErr != nil {
		return
	}
	c.out = append(c.out, data...)
	c.outCond.Broadcast()
}

// decode fills p from buffered input, consuming at least one byte from the connection, and returns the replies the
// consumed commands need.
func (c *Conn) decode(p []byte) (int, []byte, error) {
	n := 0
	var reply []byte
	for n < len(p) {
		if n > 0 || len(reply) > 0 {
			if c.reader.Buffered() == 0 {
				break
			}
		}
		b, err := c.reader.ReadByte()
		if err != nil {
			return n, reply, err
		}
		switch c.parse {
		case parseData:
			if b == cmdIAC {
				c.parse = parseIAC
				continue
			}
			if c.sawCR {
				c.sawCR = false
				// NVT sends a bare carriage return as CR NUL; the NUL is padding, not data.
				if b == 0 && !c.remoteBinary() {
					continue
				}
			}
			if b == '\r' {
				c.sawCR = true
			}
			p[n] = b
			n++
		case parseIAC:
			switch b {
			case cmdIAC:
				c.parse = parseData
				c.sawCR = false
				p[n] = cmdIAC
				n++
			case cmdWILL, cmdWONT, cmdDO, cmdDONT:
				c.command = b
				c.parse = parseOption
			case cmdSB:
				c.sb = c.sb[:0]
				c.parse = parseSB
			default:
				// NOP, GA, DM, AYT and the other single-byte commands carry nothing a terminal shows.
				c.parse = parseData
			}
		case parseOption:
			c.parse = parseData
			reply = append(reply, c.negotiate(c.command, b)...)
		case parseSB:
			if b == cmdIAC {
				c.parse = parseSBIAC
				continue
			}
			if len(c.sb) < maxSubnegotiation {
				c.sb = append(c.sb, b)
			}
		case parseSBIAC:
			switch b {
			case cmdSE:
				c.parse = parseData
				reply = append(reply, c.subnegotiate(c.sb)...)
			case cmdIAC:
				c.parse = parseSB
				if len(c.sb) < maxSubnegotiation {
					c.sb = append(c.sb, cmdIAC)
				}
			default:
				// A command inside a subnegotiation ends it without SE (RFC 855 leaves this to the receiver).
				c.parse = parseIAC
				_ = c.reader.UnreadByte()
			}
		}
	}
	return n, reply, nil
}

func (c *Conn) remoteBinary() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remote[optBinary] == stateYes
}

func (c *Conn) localBinary() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.local[optBinary] == stateYes
}

// negotiate applies one WILL/WONT/DO/DONT and returns the reply it needs, if any.
func (c *Conn) negotiate(command, option byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch command {
	case cmdDO:
		switch c.local[option] {
		case stateYes:
			return nil
		case stateWantYes:
			c.local[option] = stateYes
			return c.enabledLocked(option)
		}
		if !localSupported[option] {
			return []byte{cmdIAC, cmdWONT, option}
		}
		c.local[option] = stateYes
		return append([]byte{cmdIAC, cmdWILL, option}, c.enabledLocked(option)...)
	case cmdDONT:
		previous := c.local[option]
		c.local[option] = stateNo
		if previous == stateYes {
			return []byte{cmdIAC, cmdWONT, option}
		}
		return nil
	case cmdWILL:
		switch c.remote[option] {
		case stateYes:
			return nil
		case stateWantYes:
			c.remote[option] = stateYes
			return nil
		}
		if !remoteSupported[option] {
			return []byte{cmdIAC, cmdDONT, option}
		}
		c.remote[option] = stateYes
		return []byte{cmdIAC, cmdDO, option}
	case cmdWONT:
		previous := c.remote[option]
		c.remote[option] = stateNo
		if previous == stateYes {
			return []byte{cmdIAC, cmdDONT, option}
		}
		return nil
	}
	return nil
}

// enabledLocked returns what an option sends as soon as it is on: NAWS reports the current size right away.
func (c *Conn) enabledLocked(option byte) []byte {
	if option == optNAWS && c.cols > 0 && c.rows > 0 {
		return nawsFrame(c.cols, c.rows)
	}
	return nil
}

func (c *Conn) subnegotiate(body []byte) []byte {
	if len(body) < 2 || body[0] != optTTYPE || body[1] != ttypeSEND {
		return nil
	}
	c.mu.Lock()
	enabled := c.local[optTTYPE] == stateYes
	c.mu.Unlock()
	if !enabled {
		return nil
	}
	frame := []byte{cmdIAC, cmdSB, optTTYPE, ttypeIS}
	frame = append(frame, escapeIAC([]byte(c.options.term))...)
	return append(frame, cmdIAC, cmdSE)
}

func nawsFrame(cols, rows uint16) []byte {
	size := []byte{byte(cols >> 8), byte(cols), byte(rows >> 8), byte(rows)}
	frame := []byte{cmdIAC, cmdSB, optNAWS}
	frame = append(frame, escapeIAC(size)...)
	return append(frame, cmdIAC, cmdSE)
}

func escapeIAC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for _, b := range data {
		out = append(out, b)
		if b == cmdIAC {
			out = append(out, cmdIAC)
		}
	}
	return out
}

// Write sends user input. A data byte 255 is doubled, and outside binary mode a carriage return that is not part of
// CR LF is sent as CR NUL, as NVT requires, or as CR LF when the asset asks for it.
func (c *Conn) Write(p []byte) (int, error) {
	if err := c.closedErr(); err != nil {
		return 0, err
	}
	binary := c.localBinary()
	out := make([]byte, 0, len(p)+8)
	for i, b := range p {
		out = append(out, b)
		switch {
		case b == cmdIAC:
			out = append(out, cmdIAC)
		case b == '\r' && !binary && (i+1 >= len(p) || p[i+1] != '\n'):
			if c.options.crlf {
				out = append(out, '\n')
			} else {
				out = append(out, 0)
			}
		}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.raw.Write(out); err != nil {
		return 0, err
	}
	if c.localEcho() {
		if echo := c.echoBytes(p); len(echo) > 0 {
			c.queue(echo, false)
		}
	}
	return len(p), nil
}

func (c *Conn) localEcho() bool {
	switch c.options.echo {
	case EchoOn:
		return true
	case EchoOff:
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remote[optEcho] != stateYes
}

// echoBytes is what a terminal shows for typed input: text as typed, Enter as a new line, Backspace and DEL as an
// erased cell. Escape sequences (arrows, function keys) and other control keys show nothing. Callers hold writeMu.
func (c *Conn) echoBytes(p []byte) []byte {
	var out []byte
	for _, b := range p {
		switch c.echoParse {
		case echoEscape:
			switch b {
			case '[':
				c.echoParse = echoCSI
			case 'O':
				c.echoParse = echoSS3
			default:
				c.echoParse = echoText
			}
			continue
		case echoCSI:
			if b >= 0x40 && b <= 0x7e {
				c.echoParse = echoText
			}
			continue
		case echoSS3:
			c.echoParse = echoText
			continue
		}
		previousCR := c.echoCR
		c.echoCR = b == '\r'
		switch {
		case b == 0x1b:
			c.echoParse = echoEscape
		case b == '\r':
			out = append(out, '\r', '\n')
		case b == '\n':
			if !previousCR {
				out = append(out, '\r', '\n')
			}
		case b == 0x08 || b == 0x7f:
			out = append(out, 0x08, ' ', 0x08)
		case b == '\t' || b >= 0x20:
			out = append(out, b)
		}
	}
	return out
}

func (c *Conn) Resize(ctx context.Context, cols, rows uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.closedErr(); err != nil {
		return err
	}
	c.mu.Lock()
	c.cols, c.rows = uint16(min(cols, 0xffff)), uint16(min(rows, 0xffff))
	var frame []byte
	if c.local[optNAWS] == stateYes {
		frame = nawsFrame(c.cols, c.rows)
	}
	c.mu.Unlock()
	return c.writeRaw(frame)
}

func (c *Conn) closedErr() error {
	select {
	case <-c.done:
		return base.ErrClosed
	default:
		return nil
	}
}

func (c *Conn) finish(err error) {
	if err != nil && !errors.Is(err, io.EOF) {
		c.readErr.CompareAndSwap(nil, &err)
	}
	c.outMu.Lock()
	if c.outErr == nil {
		c.outErr = err
		if c.outErr == nil || errors.Is(err, net.ErrClosed) {
			c.outErr = io.EOF
		}
	}
	c.outCond.Broadcast()
	c.outMu.Unlock()
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.raw.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
}

// Wait blocks until the connection ends. Telnet has no exit status, so a clean close by the server returns nil.
func (c *Conn) Wait(ctx context.Context) error {
	select {
	case <-c.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := c.readErr.Load(); err != nil && !errors.Is(*err, net.ErrClosed) {
		return *err
	}
	return nil
}

func (c *Conn) CloseWrite() error {
	if closer, ok := c.raw.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return c.Close()
}

func (c *Conn) Close() error {
	c.finish(nil)
	return nil
}

func (c *Conn) Stderr() io.Reader { return strings.NewReader("") }

func (c *Conn) ID() string { return c.id }

func (c *Conn) Generation() uint64 { return c.generation }

var _ base.Channel = (*Conn)(nil)
