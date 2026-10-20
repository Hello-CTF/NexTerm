package watches

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
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

// Evaluate reports whether data matches the condition and, on a match, the
// byte range within data plus a bounded excerpt of the containing line. The
// excerpt is untrusted output data and is capped for the event envelope.
func (c Condition) Evaluate(data []byte, maxExcerptBytes int) (matched bool, from, to int64, excerpt string) {
	if maxExcerptBytes <= 0 {
		maxExcerptBytes = DefaultMaxExcerptBytes
	}
	start, length := c.locate(data)
	if length < 0 {
		return false, 0, 0, ""
	}
	return true, int64(start), int64(start + length), excerptLine(data, start, maxExcerptBytes)
}

func (c Condition) locate(data []byte) (start, length int) {
	switch c.Type {
	case ConditionContains:
		index := bytes.Index(data, []byte(c.Pattern))
		if index < 0 {
			return 0, -1
		}
		return index, len(c.Pattern)
	case ConditionRegex:
		match := regexp.MustCompile(c.Pattern).FindIndex(data)
		if match == nil {
			return 0, -1
		}
		return match[0], match[1] - match[0]
	default:
		return 0, -1
	}
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
