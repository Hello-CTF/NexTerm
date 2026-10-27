package session

import "github.com/Hello-CTF/NexTerm/internal/terminal"

// Output classes record, by stream sequence, how each byte range of a tab's
// output was fed, so every delivery — attach dumps, anchored resumes,
// recovery replays and the live fanout — can tell the client which bytes it
// must render without side effects. Live output is answered by one client,
// the answering client of input_clients.go: it is live for that client and
// answered for every other one. Live output delivered to its answering
// client is the wire default and is never announced; the list is trimmed to
// the ring's retention and changes kind only when the class or the answering
// client changes, so it stays a handful of spans.

const streamClassType = "nexterm.stream-class"

// classRetention bounds the spans kept: no delivery reaches further back than
// the scrollback ring or the recovery ring.
const classRetention = max(uint64(terminal.ScrollbackBytes), uint64(recoveryRingBytes))

const (
	// classReplay is history replayed into the tab (a recovered durable
	// tail, its mode prelude, notices inside it): render only.
	classReplay = "replay"
	// classAnswered is live output fed while no answering client was
	// attached: the headless screen already answered its queries, so a
	// client must not answer them again; other effects still apply.
	classAnswered = "answered"
)

// classSpan is the stream sequence range [From, To) of one output class.
// A stored live span (empty Kind) records the client that answered it.
type classSpan struct {
	From     uint64 `json:"from"`
	To       uint64 `json:"to"`
	Kind     string `json:"kind"`
	answerer string
}

// streamClassFrame announces one class span to a subscriber ahead of the
// bytes it covers.
type streamClassFrame struct {
	Type string `json:"type"`
	classSpan
}

type outputClasses struct {
	spans []classSpan
}

// add records [from, to) as kind; a live range (empty kind) records the
// client that answers it.
func (c *outputClasses) add(from, to uint64, kind, answerer string) {
	if to <= from {
		return
	}
	if kind != "" {
		answerer = ""
	}
	if n := len(c.spans); n > 0 && c.spans[n-1].Kind == kind && c.spans[n-1].answerer == answerer && c.spans[n-1].To == from {
		c.spans[n-1].To = to
		return
	}
	c.spans = append(c.spans, classSpan{From: from, To: to, Kind: kind, answerer: answerer})
}

// trim forgets spans that end at or before floor.
func (c *outputClasses) trim(floor uint64) {
	drop := 0
	for drop < len(c.spans) && c.spans[drop].To <= floor {
		drop++
	}
	if drop == 0 {
		return
	}
	c.spans = c.spans[drop:]
	// Reslicing leaves the dropped prefix in the backing array; copy into a
	// fresh one only once the live spans are a small part of it.
	if cap(c.spans) > 16 && len(c.spans) < cap(c.spans)/4 {
		c.spans = append([]classSpan(nil), c.spans...)
	}
}

// within returns the classes of [from, to) as client receives them: live
// ranges another client answered are answered for it, and the ranges it
// answers itself are left out as the wire default.
func (c *outputClasses) within(from, to uint64, client string) []classSpan {
	var out []classSpan
	for _, span := range c.spans {
		lo, hi := max(span.From, from), min(span.To, to)
		if lo >= hi {
			continue
		}
		kind := span.Kind
		if kind == "" {
			if span.answerer == client {
				continue
			}
			kind = classAnswered
		}
		if n := len(out); n > 0 && out[n-1].Kind == kind && out[n-1].To == lo {
			out[n-1].To = hi
			continue
		}
		out = append(out, classSpan{From: lo, To: hi, Kind: kind})
	}
	return out
}
