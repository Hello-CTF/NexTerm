// Package signals extracts terminal-standard attention signals (BEL, OSC 9,
// OSC 777, OSC 99 and OSC 133 command boundaries) from a PTY output stream.
//
// The scanner is a byte-level state machine: it never modifies, buffers or
// re-scans the stream, so the events it produces do not depend on how the
// stream is split into chunks. It is deterministic, spawns no goroutines and
// performs no I/O; time is supplied by the caller.
package signals

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

// Kind identifies an Event.
type Kind uint8

const (
	// KindBell is a BEL (0x07) that rang the terminal bell, i.e. one outside
	// any OSC or string sequence.
	KindBell Kind = iota + 1
	// KindNotify is a desktop-style notification request.
	KindNotify
	// KindCommandStart is the start of a command (OSC 133 C, or B when D arrives with no C).
	KindCommandStart
	// KindCommandEnd is the end of a command (OSC 133 D).
	KindCommandEnd
)

func (k Kind) String() string {
	switch k {
	case KindBell:
		return "bell"
	case KindNotify:
		return "notify"
	case KindCommandStart:
		return "command_start"
	case KindCommandEnd:
		return "command_end"
	}
	return "unknown"
}

// Notification sources.
const (
	SourceOSC9   = "osc9"
	SourceOSC777 = "osc777"
	SourceOSC99  = "osc99"
)

// Event is one signal observed in the stream, in stream order.
type Event struct {
	Kind Kind
	// Source, Title and Body are set for KindNotify.
	Source string
	Title  string
	Body   string
	// ExitCode and HasExitCode are set for KindCommandEnd; HasExitCode is
	// false when the shell reported the end without a code.
	ExitCode    int
	HasExitCode bool
	// Duration is set for KindCommandEnd: the time between the Feed call that
	// saw the command start and the Feed call that saw its end.
	Duration time.Duration
	// At is the timestamp passed to the Feed call that produced the event.
	At time.Time
}

// Limits. Payloads over a limit are dropped whole (never truncated) and the
// scanner resynchronises at the next terminator.
const (
	// DefaultBellInterval is the minimum spacing between emitted bells.
	DefaultBellInterval = time.Second
	// DefaultNotifyWindow and DefaultNotifyBurst allow at most Burst
	// notifications per rolling Window.
	DefaultNotifyWindow = time.Second
	DefaultNotifyBurst  = 3

	// MaxOSCPayload bounds the bytes collected for one OSC sequence.
	MaxOSCPayload = 8192
	// MaxNotifyPayload bounds an OSC 9 / OSC 777 payload.
	MaxNotifyPayload = 4096
	// MaxAssembledPayload bounds the decoded title plus body of one OSC 99
	// notification assembled from chunks.
	MaxAssembledPayload = 8192
	// MaxAssemblies bounds the number of OSC 99 notifications being
	// assembled at once; the oldest is evicted first.
	MaxAssemblies = 4
)

// Config tunes rate limits. Zero values select the defaults.
type Config struct {
	BellInterval time.Duration
	NotifyWindow time.Duration
	NotifyBurst  int
}

// Stats counts signals that were recognised but not delivered, so callers can
// keep skipped input visible.
type Stats struct {
	// Oversize counts payloads dropped for exceeding a size limit.
	Oversize uint64
	// Malformed counts recognised notification payloads that were invalid.
	Malformed uint64
	// ConEmuFiltered counts OSC 9 payloads dropped as ConEmu subcommands.
	// The filter also matches notification bodies that are only a number
	// (for example "42" or "2024;done"); that trade-off is deliberate, and
	// this counter keeps those drops visible.
	ConEmuFiltered uint64
	// RateLimited counts bells and notifications suppressed by rate limits.
	RateLimited uint64
}

type state uint8

const (
	stGround state = iota
	stEsc
	stOSC
	stOSCEsc
	stStr // DCS, SOS, PM or APC string, ignored until ST
	stStrEsc
	// C1 controls arrive UTF-8 encoded as C2 80..9F. The C2 states hold the
	// lead byte until the next byte decides which control it was.
	stC2
	stOSCC2
	stStrC2
)

// Scanner is not safe for concurrent use; feed it from the single goroutine
// that owns the output stream.
type Scanner struct {
	cfg   Config
	st    state
	buf   []byte
	over  bool // current OSC exceeded MaxOSCPayload
	stats Stats

	lastBell    time.Time
	hasBell     bool
	notifyTimes []time.Time

	cmd        *shellintegr.CommandTracker
	cmdActive  bool
	cmdStarted time.Time
	// promptEnded is set by 133;B until a C starts the command.
	promptEnded   bool
	promptEndedAt time.Time

	assemblies []*assembly
}

// NewScanner returns a scanner with the given configuration.
func NewScanner(cfg Config) *Scanner {
	if cfg.BellInterval <= 0 {
		cfg.BellInterval = DefaultBellInterval
	}
	if cfg.NotifyWindow <= 0 {
		cfg.NotifyWindow = DefaultNotifyWindow
	}
	if cfg.NotifyBurst <= 0 {
		cfg.NotifyBurst = DefaultNotifyBurst
	}
	return &Scanner{cfg: cfg, cmd: shellintegr.NewCommandTracker()}
}

// Stats returns the counters of dropped signals.
func (s *Scanner) Stats() Stats { return s.stats }

// Feed scans p without modifying it and returns the events completed by it.
// Sequences split across calls are completed by later calls. now stamps the
// events and drives rate limiting and command durations.
func (s *Scanner) Feed(p []byte, now time.Time) []Event {
	var out []Event
	for i := 0; i < len(p); {
		b := p[i]
		switch s.st {
		case stGround:
			switch b {
			case 0x07:
				out = s.bell(out, now)
			case 0x1b:
				s.st = stEsc
			case 0xc2:
				s.st = stC2
			}
		case stC2:
			s.st = stGround
			switch b {
			case 0x90, 0x98, 0x9e, 0x9f: // DCS, SOS, PM, APC
				s.st = stStr
			case 0x9d: // OSC
				s.beginOSC()
			case 0x9c: // ST
			default:
				continue
			}
		case stEsc:
			switch b {
			case ']':
				s.beginOSC()
			case 'P', 'X', '^', '_':
				s.st = stStr
			case 0x1b:
				// Stay in stEsc.
			case 0x07:
				out = s.bell(out, now)
			default:
				s.st = stGround
			}
		case stOSC:
			switch b {
			case 0x07:
				out = s.finishOSC(out, now)
			case 0x1b:
				s.st = stOSCEsc
			case 0xc2:
				s.st = stOSCC2
			case 0x18, 0x1a:
				s.resetOSC()
			default:
				s.collect(b)
			}
		case stOSCC2:
			if b == 0x9c { // C1 ST
				out = s.finishOSC(out, now)
				break
			}
			s.collect(0xc2)
			s.st = stOSC
			continue
		case stOSCEsc:
			if b == '\\' {
				out = s.finishOSC(out, now)
				break
			}
			// ESC inside an OSC aborts it; this byte starts a new escape.
			// xterm.js instead ends the OSC at any ESC. Aborting is the
			// deliberate conservative choice: a half-written or interleaved
			// sequence never produces a notification from truncated text.
			s.resetOSC()
			s.st = stEsc
			continue
		case stStr:
			switch b {
			case 0x1b:
				s.st = stStrEsc
			case 0xc2:
				s.st = stStrC2
			case 0x18, 0x1a:
				s.st = stGround
			}
		case stStrC2:
			if b == 0x9c {
				s.st = stGround
				break
			}
			s.st = stStr
			continue
		case stStrEsc:
			if b == '\\' {
				s.st = stGround
				break
			}
			s.st = stEsc
			continue
		}
		i++
	}
	return out
}

func (s *Scanner) beginOSC() {
	s.st = stOSC
	s.buf = s.buf[:0]
	s.over = false
}

func (s *Scanner) resetOSC() {
	s.st = stGround
	s.buf = s.buf[:0]
	s.over = false
}

func (s *Scanner) collect(b byte) {
	if s.over {
		return
	}
	if len(s.buf) >= MaxOSCPayload {
		s.over = true
		s.buf = s.buf[:0]
		return
	}
	s.buf = append(s.buf, b)
}

func (s *Scanner) finishOSC(out []Event, now time.Time) []Event {
	payload, over := s.buf, s.over
	s.st = stGround
	s.over = false
	if over {
		s.stats.Oversize++
		s.buf = s.buf[:0]
		return out
	}
	out = s.dispatch(out, payload, now)
	s.buf = s.buf[:0]
	return out
}

func (s *Scanner) bell(out []Event, now time.Time) []Event {
	if s.hasBell && !now.Before(s.lastBell) && now.Sub(s.lastBell) < s.cfg.BellInterval {
		s.stats.RateLimited++
		return out
	}
	s.lastBell, s.hasBell = now, true
	return append(out, Event{Kind: KindBell, At: now})
}

// allowNotify applies a rolling-window burst limit.
func (s *Scanner) allowNotify(now time.Time) bool {
	kept := s.notifyTimes[:0]
	for _, t := range s.notifyTimes {
		if d := now.Sub(t); d >= 0 && d < s.cfg.NotifyWindow {
			kept = append(kept, t)
		}
	}
	s.notifyTimes = kept
	if len(kept) >= s.cfg.NotifyBurst {
		s.stats.RateLimited++
		return false
	}
	s.notifyTimes = append(s.notifyTimes, now)
	return true
}

// notify emits a notification. Emptiness and the rate limit are checked on
// the raw text first so a suppressed flood does not pay for sanitising.
func (s *Scanner) notify(out []Event, source string, rawTitle, rawBody []byte, now time.Time) []Event {
	if len(rawTitle) == 0 && len(rawBody) == 0 {
		s.stats.Malformed++
		return out
	}
	if !s.allowNotify(now) {
		return out
	}
	title, body := clean(string(rawTitle)), clean(string(rawBody))
	if title == "" && body == "" {
		s.stats.Malformed++
		return out
	}
	return append(out, Event{Kind: KindNotify, Source: source, Title: title, Body: body, At: now})
}

// command tracks the OSC 133 lifecycle. A command starts at C (preexec: the
// command is actually running); B only marks the end of the prompt, which can
// be long before the user presses Enter. If a D arrives without any C, the
// command is reported from B's timestamp instead.
func (s *Scanner) command(out []Event, payload []byte, now time.Time) []Event {
	r, ok := s.cmd.ObservePayload(payload)
	if !ok {
		return out
	}
	switch r.Letter {
	case 'A':
		// A new prompt without a D: the previous command is gone.
		s.cmdActive, s.promptEnded = false, false
	case 'B':
		if !s.cmdActive {
			s.promptEnded, s.promptEndedAt = true, now
		}
	case 'C':
		if !s.cmdActive {
			s.cmdActive, s.cmdStarted, s.promptEnded = true, now, false
			out = append(out, Event{Kind: KindCommandStart, At: now})
		}
	case 'D':
		if !s.cmdActive && s.promptEnded {
			s.cmdActive, s.cmdStarted = true, s.promptEndedAt
			out = append(out, Event{Kind: KindCommandStart, At: s.promptEndedAt})
		}
		s.promptEnded = false
		if s.cmdActive {
			s.cmdActive = false
			d := max(now.Sub(s.cmdStarted), 0)
			out = append(out, Event{
				Kind: KindCommandEnd, ExitCode: r.ExitCode, HasExitCode: r.HasExitCode,
				Duration: d, At: now,
			})
		}
	}
	return out
}
