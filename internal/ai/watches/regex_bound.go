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
// proper prefix of a potential match, bounded by capacity+1. The search runs
// on the whole buffer with a lazy rune-counted prefix: the leftmost match of
// the prefix regex at or after the anchor, so the true left context grounds
// assertions while a match can never start before the anchor — context bytes
// are not eligible live prefixes.
func scanLiveSuffix(contextTail, data []byte, capacity int, prefixBody string) int {
	wrapper := regexp.MustCompile("^(?:[\\s\\S]{" + strconv.Itoa(utf8.RuneCount(contextTail)) + ",}?)(" + prefixBody + ")$")
	input := make([]byte, 0, len(contextTail)+len(data))
	input = append(input, contextTail...)
	input = append(input, data...)
	loc := wrapper.FindStringSubmatchIndex(string(input))
	if loc == nil {
		return 0
	}
	return len(input) - loc[2]
}

func isRuneAlignedStart(data []byte, index int) bool {
	return index >= 0 && (index == 0 || data[index] < 0x80 || data[index] > 0xBF)
}

// neverMatch is a regex that matches nothing; it represents an empty prefix
// language when building prefix regexes.
const neverMatch = `\b\B`

// emptyPrefix is a regex matching only the empty string; it represents the
// empty proper prefix that every subexpression has, which keeps node-boundary
// prefixes expressible in concatenations.
const emptyPrefix = `(?:)`

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
			return emptyPrefix, true
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
		return emptyPrefix, true
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
			branches = append(branches, "(?:"+prefix+")(?:"+branch+")")
			prefix += "(?:" + sub.String() + ")"
		}
		return "(?:" + strings.Join(branches, "|") + ")", true
	case syntax.OpAlternate:
		var branches []string
		for _, sub := range re.Sub {
			branch, ok := prefixPattern(sub)
			if !ok {
				return "", false
			}
			branches = append(branches, branch)
		}
		return "(?:" + strings.Join(branches, "|") + ")", true
	case syntax.OpStar, syntax.OpPlus:
		branch, ok := prefixPattern(re.Sub[0])
		if !ok {
			return "", false
		}
		// Proper prefixes of rⁿ are complete repetitions followed by a
		// partial one: full(r)* · prefix(r).
		return "(?:" + re.Sub[0].String() + ")*(?:" + branch + ")", true
	case syntax.OpQuest:
		return prefixPattern(re.Sub[0])
	case syntax.OpRepeat:
		branch, ok := prefixPattern(re.Sub[0])
		if !ok {
			return "", false
		}
		if re.Max < 0 {
			return "(?:" + re.Sub[0].String() + ")*(?:" + branch + ")", true
		}
		if re.Max <= 1 {
			return branch, true
		}
		return "(?:" + re.Sub[0].String() + "){0," + strconv.Itoa(re.Max-1) + "}(?:" + branch + ")", true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return emptyPrefix, true
	default:
		return "", false
	}
}

// trailingRightContextDependent reports whether the pattern's matches can
// depend on bytes beyond the match end: end-of-text/end-of-line assertions
// or a word-boundary assertion at the trailing edge. Such matches may not
// fire at the current chunk end — the following bytes (or an explicit EOF)
// must ground them first.
func trailingRightContextDependent(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEndText, syntax.OpEndLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpCapture:
		return trailingRightContextDependent(re.Sub[0])
	case syntax.OpConcat:
		// A trailing empty group hides nothing: walk from the end and judge
		// the last input-consuming subexpression.
		for i := len(re.Sub) - 1; i >= 0; i-- {
			sub := re.Sub[i]
			if isRightContextAssertion(sub) {
				return true
			}
			if !alwaysEmpty(sub) {
				return false
			}
		}
		return false
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if trailingRightContextDependent(sub) {
				return true
			}
		}
		return false
	case syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		return trailingRightContextDependent(re.Sub[0])
	default:
		return false
	}
}

func isRightContextAssertion(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEndText, syntax.OpEndLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	default:
		return false
	}
}

// alwaysEmpty reports whether the subexpression can only match the empty
// string (it never consumes input).
func alwaysEmpty(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpCapture:
		return alwaysEmpty(re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !alwaysEmpty(sub) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if !alwaysEmpty(sub) {
				return false
			}
		}
		return true
	case syntax.OpQuest, syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		return alwaysEmpty(re.Sub[0])
	default:
		return false
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
