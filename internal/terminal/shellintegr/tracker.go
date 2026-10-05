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
	data := p
	if len(t.pending) > 0 {
		data = make([]byte, 0, len(t.pending)+len(p))
		data = append(data, t.pending...)
		data = append(data, p...)
		t.pending = nil
	}
	latest := ""
	found := false
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			break
		}
		seq := data[i:]
		if len(seq) < 2 {
			t.pending = append(t.pending[:0], seq...)
			break
		}
		if seq[1] != ']' {
			data = seq[2:]
			continue
		}
		end, next := oscSequenceEnd(seq[2:])
		if end < 0 {
			if len(seq) > maxPending {
				break
			}
			t.pending = append(t.pending[:0], seq...)
			break
		}
		if cwd, ok := parseOSC7(seq[2 : 2+end]); ok {
			t.cwd = cwd
			latest, found = cwd, true
		}
		data = seq[next:]
	}
	return latest, found
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
