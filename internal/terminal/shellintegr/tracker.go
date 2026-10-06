package shellintegr

import (
	"bytes"
	"net/url"
)

// maxPending bounds the buffered tail of an incomplete escape sequence.
// OSC 7 payloads are far smaller; anything longer is dropped and the scan
// resynchronizes on the next ESC.
const maxPending = 4096

// Tracker extracts OSC 7 cwd reports from a terminal output stream as it is
// fed through the recording path. Observe only reads its input; the bytes
// handed to the recorder, ring buffer, and replay stay untouched.
//
// Tracker is not safe for concurrent use; feed it from the single goroutine
// that owns the output stream.
type Tracker struct {
	pending []byte
	cwd     string
}

func NewTracker() *Tracker { return &Tracker{} }

// CWD returns the most recently reported working directory, or "" before the
// first report.
func (t *Tracker) CWD() string { return t.cwd }

// Observe scans p for OSC 7 reports without modifying p. It returns the last
// cwd reported within p, if any; reports split across chunk boundaries are
// picked up once the completing chunk arrives.
func (t *Tracker) Observe(p []byte) (string, bool) {
	latest := ""
	found := false
	tail := scanOSC(pendingJoin(t.pending, p), func(payload []byte) {
		if cwd, ok := parseOSC7(payload); ok {
			t.cwd = cwd
			latest, found = cwd, true
		}
	})
	t.pending = append(t.pending[:0], tail...)
	return latest, found
}

// pendingJoin prepends any buffered incomplete sequence to p so a split
// sequence can be rescanned as a whole.
func pendingJoin(pending, p []byte) []byte {
	if len(pending) == 0 {
		return p
	}
	data := make([]byte, 0, len(pending)+len(p))
	data = append(data, pending...)
	data = append(data, p...)
	return data
}

// scanOSC consumes data, invoking handle with the payload of each complete
// OSC sequence. It returns the trailing bytes of an incomplete sequence for
// the caller to buffer, or nil when the tail is dropped because an overlong
// sequence overflowed maxPending.
func scanOSC(data []byte, handle func(payload []byte)) []byte {
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			return nil
		}
		seq := data[i:]
		if len(seq) < 2 {
			return seq
		}
		if seq[1] != ']' {
			data = seq[2:]
			continue
		}
		end, next := oscSequenceEnd(seq[2:])
		if end < 0 {
			if len(seq) > maxPending {
				return nil
			}
			return seq
		}
		handle(seq[2 : 2+end])
		data = seq[next:]
	}
	return nil
}

// oscSequenceEnd scans an OSC payload for BEL or ST, returning the payload
// end relative to the payload start and the index just past the terminator
// relative to the ESC. It returns -1 when the sequence is incomplete.
func oscSequenceEnd(payload []byte) (end, next int) {
	for j := 0; j < len(payload); j++ {
		switch payload[j] {
		case 0x07:
			return j, j + 3
		case 0x1b:
			if j+1 >= len(payload) {
				return -1, 0
			}
			if payload[j+1] == '\\' {
				return j, j + 4
			}
		}
	}
	return -1, 0
}

func parseOSC7(payload []byte) (string, bool) {
	code, rest, ok := bytes.Cut(payload, []byte(";"))
	if !ok || string(code) != "7" {
		return "", false
	}
	u, err := url.Parse(string(rest))
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	return u.Path, true
}
