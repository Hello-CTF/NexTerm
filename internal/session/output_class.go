package session

import "github.com/Hello-CTF/NexTerm/internal/terminal"

// Output classes record, by stream sequence, how each byte range of a tab's
// output was fed, so every delivery — attach dumps, anchored resumes,
// recovery replays and the live fanout — can tell the client which bytes it
// must render without side effects. Live output answered by the client is the
// default and is not stored; the list only holds the other ranges and is
// trimmed to the ring's retention, so it stays a handful of spans.

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
type classSpan struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
	Kind string `json:"kind"`
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

// add records [from, to) as kind; live ranges are not stored.
func (c *outputClasses) add(from, to uint64, kind string) {
	if kind == "" || to <= from {
		return
	}
	if n := len(c.spans); n > 0 && c.spans[n-1].Kind == kind && c.spans[n-1].To == from {
		c.spans[n-1].To = to
		return
	}
	c.spans = append(c.spans, classSpan{From: from, To: to, Kind: kind})
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

// within returns the stored spans clipped to [from, to).
func (c *outputClasses) within(from, to uint64) []classSpan {
	var out []classSpan
	for _, span := range c.spans {
		lo, hi := max(span.From, from), min(span.To, to)
		if lo < hi {
			out = append(out, classSpan{From: lo, To: hi, Kind: span.Kind})
		}
	}
	return out
}
