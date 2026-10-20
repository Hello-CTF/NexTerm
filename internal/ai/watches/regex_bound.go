package watches

import (
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// regexPrefixParticipation returns a conservative upper bound (in bytes,
// saturated at limit) of the longest proper prefix of any string matched by
// pattern, and false when that participation is unbounded (an unbounded
// repetition can always extend a partial match). The bound drives the
// durable carry window; unbounded patterns are clamped to the caller's
// capacity so buffering stays bounded.
func regexPrefixParticipation(pattern string, limit int64) (int64, bool) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return 0, true
	}
	return maxProperPrefix(re, limit)
}

// maxProperPrefix bounds the longest proper prefix of any match of re.
// Results are saturated at limit; ok=false means unbounded.
func maxProperPrefix(re *syntax.Regexp, limit int64) (int64, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		return saturate(literalBytes(re)-1, limit), true
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return 0, true
	case syntax.OpCapture:
		return maxProperPrefix(re.Sub[0], limit)
	case syntax.OpConcat:
		var sum, best int64
		for _, sub := range re.Sub {
			prefix, prefixFinite := maxProperPrefix(sub, limit)
			length, lengthFinite := maxMatchLen(sub, limit)
			if !prefixFinite || !lengthFinite {
				return 0, false
			}
			if sum+prefix > best {
				best = sum + prefix
			}
			sum = saturate(sum+length, limit)
		}
		return best, true
	case syntax.OpAlternate:
		var best int64
		for _, sub := range re.Sub {
			prefix, finite := maxProperPrefix(sub, limit)
			if !finite {
				return 0, false
			}
			if prefix > best {
				best = prefix
			}
		}
		return best, true
	case syntax.OpStar, syntax.OpPlus:
		if canConsumeNonEmpty(re.Sub[0]) {
			return 0, false
		}
		return maxProperPrefix(re.Sub[0], limit)
	case syntax.OpQuest:
		return maxProperPrefix(re.Sub[0], limit)
	case syntax.OpRepeat:
		if re.Max < 0 {
			if canConsumeNonEmpty(re.Sub[0]) {
				return 0, false
			}
			return maxProperPrefix(re.Sub[0], limit)
		}
		length, finite := maxMatchLen(re.Sub[0], limit)
		if !finite {
			return 0, false
		}
		prefix, prefixFinite := maxProperPrefix(re.Sub[0], limit)
		if !prefixFinite {
			return 0, false
		}
		best := prefix
		if re.Max > 1 {
			best = saturatePrefix(int64(re.Max-1)*length+prefix, limit)
		}
		return best, true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0, true
	default:
		return 0, false
	}
}

// maxMatchLen bounds the longest possible match of re in bytes, saturated at
// limit; ok=false means unbounded.
func maxMatchLen(re *syntax.Regexp, limit int64) (int64, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		return saturate(literalBytes(re), limit), true
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return 4, true
	case syntax.OpCapture:
		return maxMatchLen(re.Sub[0], limit)
	case syntax.OpConcat:
		var sum int64
		for _, sub := range re.Sub {
			length, finite := maxMatchLen(sub, limit)
			if !finite {
				return 0, false
			}
			sum = saturate(sum+length, limit)
		}
		return sum, true
	case syntax.OpAlternate:
		var best int64
		for _, sub := range re.Sub {
			length, finite := maxMatchLen(sub, limit)
			if !finite {
				return 0, false
			}
			if length > best {
				best = length
			}
		}
		return best, true
	case syntax.OpStar, syntax.OpPlus:
		if canConsumeNonEmpty(re.Sub[0]) {
			return 0, false
		}
		return 0, true
	case syntax.OpQuest:
		return maxMatchLen(re.Sub[0], limit)
	case syntax.OpRepeat:
		if re.Max < 0 {
			if canConsumeNonEmpty(re.Sub[0]) {
				return 0, false
			}
			return 0, true
		}
		length, finite := maxMatchLen(re.Sub[0], limit)
		if !finite {
			return 0, false
		}
		return saturate(int64(re.Max)*length, limit), true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0, true
	default:
		return 0, false
	}
}

// canConsumeNonEmpty reports whether the subexpression can match any
// non-empty string; a nullable subexpression like a? still consumes input,
// so repetitions of it are unbounded.
func canConsumeNonEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpLiteral:
		return len(re.Rune) > 0
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return true
	case syntax.OpCapture:
		return canConsumeNonEmpty(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if canConsumeNonEmpty(sub) {
				return true
			}
		}
		return false
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return canConsumeNonEmpty(re.Sub[0])
	default:
		return false
	}
}

// literalBytes returns the literal's encoded length in bytes; for
// case-folded literals every rune contributes the widest encoding in its
// SimpleFold orbit (k and K are equivalent, so (?i)k is three bytes wide).
func literalBytes(re *syntax.Regexp) int64 {
	var total int64
	for _, r := range re.Rune {
		total += int64(runeWidth(re, r))
	}
	return total
}

func runeWidth(re *syntax.Regexp, r rune) int {
	width := utf8.RuneLen(r)
	if re.Flags&syntax.FoldCase == 0 {
		return width
	}
	for folded, steps := unicode.SimpleFold(r), 0; folded != r && steps < 32; folded, steps = unicode.SimpleFold(folded), steps+1 {
		if next := utf8.RuneLen(folded); next > width {
			width = next
		}
	}
	return width
}

// scanLiveSuffix returns the length of the longest suffix of data that is a
// proper prefix of a potential match, bounded by capacity+1. previousLive is
// the live suffix length from the previous round and added the number of new
// complete bytes, so the scan starts from the longest possible extension;
// the result may exceed capacity, which the caller reports as overflow.
// Only rune-aligned suffixes are considered: valid matches never start
// inside a rune.
func scanLiveSuffix(data []byte, previousLive, added, capacity int, prefixRe *regexp.Regexp) int {
	limit := min(len(data), capacity+1)
	candidate := min(previousLive+added, limit)
	if candidate > 0 && isRuneAlignedStart(data, len(data)-candidate) && prefixRe.MatchString(string(data[len(data)-candidate:])) {
		return candidate
	}
	for length := candidate - 1; length >= 1; length-- {
		if !isRuneAlignedStart(data, len(data)-length) {
			continue
		}
		if prefixRe.MatchString(string(data[len(data)-length:])) {
			return length
		}
	}
	return 0
}

func isRuneAlignedStart(data []byte, index int) bool {
	return index >= 0 && (index == 0 || data[index] < 0x80 || data[index] > 0xBF)
}

// neverMatch is a regex that matches nothing; it represents an empty prefix
// language when building prefix regexes.
const neverMatch = `\b\B`

// regexPrefixPattern builds a regular expression matching the proper
// prefixes of pattern's language (including the empty prefix), or false when
// the pattern uses an unsupported construct. The result may itself match an
// infinite language (unbounded repetitions); it is only used to test
// whether a concrete suffix could still grow into a match.
func regexPrefixPattern(pattern string) (string, bool) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", false
	}
	return prefixPattern(re)
}

func prefixPattern(re *syntax.Regexp) (string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		runes := []rune(string(re.Rune))
		if len(runes) <= 1 {
			return neverMatch, true
		}
		var b strings.Builder
		if re.Flags&syntax.FoldCase != 0 {
			b.WriteString("(?i:")
		}
		b.WriteString("(?:")
		for length := 0; length < len(runes); length++ {
			if length > 0 {
				b.WriteByte('|')
			}
			b.WriteString(regexp.QuoteMeta(string(runes[:length])))
		}
		b.WriteByte(')')
		if re.Flags&syntax.FoldCase != 0 {
			b.WriteByte(')')
		}
		return b.String(), true
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return neverMatch, true
	case syntax.OpCapture:
		return prefixPattern(re.Sub[0])
	case syntax.OpConcat:
		var branches []string
		prefix := ""
		for _, sub := range re.Sub {
			branch, ok := prefixPattern(sub)
			if !ok {
				return "", false
			}
			if branch != neverMatch {
				branches = append(branches, "(?:"+prefix+")(?:"+branch+")")
			}
			prefix += "(?:" + sub.String() + ")"
		}
		if len(branches) == 0 {
			return neverMatch, true
		}
		return "(?:" + strings.Join(branches, "|") + ")", true
	case syntax.OpAlternate:
		var branches []string
		for _, sub := range re.Sub {
			branch, ok := prefixPattern(sub)
			if !ok {
				return "", false
			}
			if branch != neverMatch {
				branches = append(branches, branch)
			}
		}
		if len(branches) == 0 {
			return neverMatch, true
		}
		return "(?:" + strings.Join(branches, "|") + ")", true
	case syntax.OpStar, syntax.OpPlus:
		branch, ok := prefixPattern(re.Sub[0])
		if !ok {
			return "", false
		}
		full := re.Sub[0].String()
		return "(?:(?:" + branch + ")(?:" + full + ")*|(?:" + full + ")+)", true
	case syntax.OpQuest:
		return prefixPattern(re.Sub[0])
	case syntax.OpRepeat:
		branch, ok := prefixPattern(re.Sub[0])
		if !ok {
			return "", false
		}
		full := re.Sub[0].String()
		if re.Max < 0 {
			return "(?:(?:" + branch + ")(?:" + full + ")*|(?:" + full + ")+)", true
		}
		if re.Max <= 1 {
			return branch, true
		}
		return "(?:(?:" + branch + ")(?:" + full + "){0," + strconv.Itoa(re.Max-1) + "}|(?:" + full + "){1," + strconv.Itoa(re.Max-1) + "})", true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return neverMatch, true
	default:
		return "", false
	}
}

func saturate(value, limit int64) int64 {
	if value > limit {
		return limit
	}
	return value
}

func saturatePrefix(value, limit int64) int64 {
	if value > limit || value < 0 {
		return limit
	}
	return value
}
