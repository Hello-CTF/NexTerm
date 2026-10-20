package watches

import (
	"bytes"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxConditionPatternBytes = 1024

// Validate rejects unknown condition types, empty or oversized patterns and
// invalid regular expressions before anything is persisted.
func (c Condition) Validate() error {
	if c.Pattern == "" {
		return fmt.Errorf("%w: 条件模式不能为空", ErrInvalidCondition)
	}
	if len(c.Pattern) > maxConditionPatternBytes {
		return fmt.Errorf("%w: 条件模式超过 %d 字节", ErrInvalidCondition, maxConditionPatternBytes)
	}
	switch c.Type {
	case ConditionContains:
		return nil
	case ConditionRegex:
		if _, err := regexp.Compile(c.Pattern); err != nil {
			return fmt.Errorf("%w: 正则表达式无效: %v", ErrInvalidCondition, err)
		}
		return nil
	default:
		return fmt.Errorf("%w: 未知条件类型 %q", ErrInvalidCondition, c.Type)
	}
}

// exactLiveSuffix returns the length of the longest suffix of data that is
// a proper prefix of pattern, i.e. the exact number of trailing bytes that
// may still participate in a future literal match.
func exactLiveSuffix(data []byte, pattern string) int {
	limit := min(len(pattern)-1, len(data))
	for length := limit; length >= 1; length-- {
		if string(data[len(data)-length:]) == pattern[:length] {
			return length
		}
	}
	return 0
}

// incompleteUTF8Tail returns the length of the trailing bytes that form a
// proper prefix of a UTF-8 encoding split by a chunk boundary (0-3 bytes).
// Genuinely invalid bytes are not an incomplete tail: they are complete
// RuneError data and stay in evaluation.
func incompleteUTF8Tail(buffer []byte) int {
	n := len(buffer)
	if n == 0 || buffer[n-1] < utf8.RuneSelf {
		return 0
	}
	start := n - 1
	for start > 0 && buffer[start] >= 0x80 && buffer[start] <= 0xBF {
		start--
	}
	lead := buffer[start]
	if lead < 0xC2 || lead > 0xF4 {
		return 0
	}
	if !utf8.FullRune(buffer[start:]) {
		return n - start
	}
	return 0
}

// carryBound returns how many trailing bytes of an evaluated buffer must be
// retained so a match split across an arbitrary chunk boundary still fires
// later: a partial pattern inside the window is re-evaluated together with
// the next chunk. The window is bounded by capacity and by the condition's
// own partial-match participation, so buffering never grows without limit.
func (c Condition) carryBound(capacity, bufferLen int) int {
	if capacity <= 0 || bufferLen <= 0 {
		return 0
	}
	bound := c.maxCarry(capacity)
	if bound > bufferLen {
		bound = bufferLen
	}
	return bound
}

// maxCarry is the condition's partial-match participation capped at
// capacity: exact for literal contains patterns, and a conservative
// prefix-participation bound for regular expressions (unbounded repetition
// clamps to capacity).
func (c Condition) maxCarry(capacity int) int {
	switch c.Type {
	case ConditionContains:
		return min(capacity, len(c.Pattern)-1)
	case ConditionRegex:
		bound, finite := regexPrefixParticipation(c.Pattern, int64(capacity))
		if !finite || bound >= int64(capacity) {
			return capacity
		}
		return int(bound)
	default:
		return 0
	}
}

// Evaluate reports whether data matches the condition and, on a match, the
// byte range within data plus a bounded excerpt of the containing line.
// minStart is the first byte position eligible to start a match (the engine
// passes the length of the prepended left context): earlier starts belong to
// already-settled history, and — unlike filtering a non-overlapping match
// list — a filtered context match can never consume the eligible region,
// because the search itself is anchored at or after minStart with the full
// left context visible. The excerpt is untrusted output data and is capped
// for the event envelope.
//
// A match that ends exactly at the end of data is only reported when it
// cannot depend on what follows, or when eof marks the true stream end:
// regular expressions with a trailing end-of-text/end-of-line or
// word-boundary assertion are deferred, because the next bytes (or EOF)
// must ground them first and an outbox event can never be retracted.
func (c Condition) Evaluate(data []byte, minStart, maxExcerptBytes int, eof bool) (matched bool, from, to int64, excerpt string) {
	if maxExcerptBytes <= 0 {
		maxExcerptBytes = DefaultMaxExcerptBytes
	}
	if minStart < 0 {
		minStart = 0
	}
	if minStart > len(data) {
		return false, 0, 0, ""
	}
	switch c.Type {
	case ConditionContains:
		index := bytes.Index(data[minStart:], []byte(c.Pattern))
		if index < 0 {
			return false, 0, 0, ""
		}
		start := minStart + index
		return true, int64(start), int64(start + len(c.Pattern)), excerptLine(data, start, maxExcerptBytes)
	case ConditionRegex:
		// The lazy prefix anchors the search at or after minStart while the
		// whole buffer stays visible, so ^/\b assertions at the match start
		// see the true preceding bytes. The prefix counts runes, and minStart
		// is a rune boundary, so the rune count of the prefix is exact.
		wrapper := regexp.MustCompile("^(?:[\\s\\S]{" + strconv.Itoa(utf8.RuneCount(data[:minStart])) + ",}?)(" + c.Pattern + ")")
		loc := wrapper.FindStringSubmatchIndex(string(data))
		if loc == nil {
			return false, 0, 0, ""
		}
		start, end := loc[2], loc[3]
		if end == len(data) && !eof && regexTrailingRightContextDependent(c.Pattern) {
			// The right edge of the match is unverifiable until the next
			// bytes (or EOF) ground it; the carry keeps it for re-evaluation.
			return false, 0, 0, ""
		}
		return true, int64(start), int64(end), excerptLine(data, start, maxExcerptBytes)
	default:
		return false, 0, 0, ""
	}
}

// regexTrailingRightContextDependent reports whether matches of the pattern
// can depend on bytes beyond the match end.
func regexTrailingRightContextDependent(pattern string) bool {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return false
	}
	return trailingRightContextDependent(re)
}

// excerptLine extracts the line around the match start and truncates it to
// the bound with a visible marker so consumers can tell truncation happened.
func excerptLine(data []byte, matchStart, maxBytes int) string {
	lineStart := bytes.LastIndexByte(data[:matchStart], '\n') + 1
	lineEnd := len(data)
	if index := bytes.IndexByte(data[matchStart:], '\n'); index >= 0 {
		lineEnd = matchStart + index
	}
	line := strings.TrimRight(string(data[lineStart:lineEnd]), "\r")
	if len(line) <= maxBytes {
		return line
	}
	return line[:maxBytes] + "…[truncated]"
}
