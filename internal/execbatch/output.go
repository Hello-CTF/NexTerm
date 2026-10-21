package execbatch

import "unicode/utf8"

const (
	// DefaultOutputHeadBytes and DefaultOutputTailBytes bound how much of each
	// stream is retained for progress reporting: the beginning and the end of
	// the output, with the middle dropped once the bounds are exceeded.
	DefaultOutputHeadBytes = 4 << 10
	DefaultOutputTailBytes = 4 << 10
)

// OutputRetention bounds retained output per stream. Head keeps the first
// bytes written, Tail keeps the last bytes written, and anything in between
// is dropped. Zero or negative bounds disable that side.
type OutputRetention struct {
	Head int
	Tail int
}

// DefaultOutputRetention is the retention used when a caller does not choose
// explicit bounds.
var DefaultOutputRetention = OutputRetention{Head: DefaultOutputHeadBytes, Tail: DefaultOutputTailBytes}

// OutputSnapshot is a point-in-time view of a retained stream. Head and Tail
// are trimmed to valid UTF-8 rune boundaries; DroppedBytes counts every byte
// that is no longer retained, so Truncated reports whether the stream lost
// any content relative to what was written.
type OutputSnapshot struct {
	Head         string `json:"head"`
	Tail         string `json:"tail"`
	TotalBytes   int    `json:"totalBytes"`
	DroppedBytes int    `json:"droppedBytes"`
	Truncated    bool   `json:"truncated"`
}

// RetainedOutput accumulates one output stream under bounded head/tail
// retention. It is not safe for concurrent use; callers serialize writes per
// stream (the progress tracker does).
type RetainedOutput struct {
	retention OutputRetention
	head      []byte
	tail      []byte
	total     int
}

func NewRetainedOutput(retention OutputRetention) *RetainedOutput {
	if retention.Head < 0 {
		retention.Head = 0
	}
	if retention.Tail < 0 {
		retention.Tail = 0
	}
	return &RetainedOutput{head: make([]byte, 0, retention.Head), tail: make([]byte, 0, retention.Tail), retention: retention}
}

// Write appends one chunk of the stream, filling the head bound first and
// keeping only the last Tail bytes afterwards.
func (r *RetainedOutput) Write(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	r.total += len(chunk)
	if headRoom := r.retention.Head - len(r.head); headRoom > 0 {
		kept := chunk
		if len(kept) > headRoom {
			kept = kept[:headRoom]
		}
		r.head = append(r.head, kept...)
		chunk = chunk[len(kept):]
	}
	if len(chunk) == 0 || r.retention.Tail == 0 {
		return
	}
	r.tail = append(r.tail, chunk...)
	if excess := len(r.tail) - r.retention.Tail; excess > 0 {
		r.tail = append(r.tail[:0], r.tail[excess:]...)
	}
}

// Snapshot returns the retained head and tail, trimmed so the head does not
// end and the tail neither starts nor ends inside a multi-byte rune. The
// underlying bytes of an incomplete trailing rune stay buffered, so a later
// Write that completes the rune restores the full content and accounting;
// until then DroppedBytes counts the held-back bytes as dropped.
func (r *RetainedOutput) Snapshot() OutputSnapshot {
	head := trimIncompleteRunetail(r.head)
	tail := trimIncompleteRunetail(trimIncompleteRuneHead(r.tail))
	dropped := r.total - len(head) - len(tail)
	return OutputSnapshot{
		Head:         string(head),
		Tail:         string(tail),
		TotalBytes:   r.total,
		DroppedBytes: dropped,
		Truncated:    dropped > 0,
	}
}

// trimIncompleteRunetail drops a trailing partial rune so the kept prefix
// ends on a rune boundary. Genuinely invalid trailing bytes are dropped too.
func trimIncompleteRunetail(p []byte) []byte {
	for len(p) > 0 {
		if r, size := utf8.DecodeLastRune(p); r != utf8.RuneError || size != 1 {
			return p
		}
		end := len(p) - 1
		for end > 0 && !utf8.RuneStart(p[end]) {
			end--
		}
		p = p[:end]
	}
	return p
}

// trimIncompleteRuneHead drops leading continuation bytes whose rune start
// fell outside the retained tail window.
func trimIncompleteRuneHead(p []byte) []byte {
	start := 0
	for start < len(p) && !utf8.RuneStart(p[start]) {
		start++
	}
	return p[start:]
}

// TargetOutput retains the stdout and stderr of one target independently
// under the same bounds.
type TargetOutput struct {
	Stdout *RetainedOutput
	Stderr *RetainedOutput
}

func NewTargetOutput(retention OutputRetention) *TargetOutput {
	return &TargetOutput{Stdout: NewRetainedOutput(retention), Stderr: NewRetainedOutput(retention)}
}

func (t *TargetOutput) WriteStdout(chunk []byte) { t.Stdout.Write(chunk) }
func (t *TargetOutput) WriteStderr(chunk []byte) { t.Stderr.Write(chunk) }

// TargetOutputSnapshot pairs the stdout and stderr snapshots of one target.
type TargetOutputSnapshot struct {
	Stdout OutputSnapshot `json:"stdout"`
	Stderr OutputSnapshot `json:"stderr"`
}

func (t *TargetOutput) Snapshot() TargetOutputSnapshot {
	return TargetOutputSnapshot{Stdout: t.Stdout.Snapshot(), Stderr: t.Stderr.Snapshot()}
}
