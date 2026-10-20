package watches

import (
	"regexp/syntax"
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
		if minMatchLen(re.Sub[0]) > 0 {
			return 0, false
		}
		return maxProperPrefix(re.Sub[0], limit)
	case syntax.OpQuest:
		return maxProperPrefix(re.Sub[0], limit)
	case syntax.OpRepeat:
		if re.Max < 0 {
			if minMatchLen(re.Sub[0]) > 0 {
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
		if minMatchLen(re.Sub[0]) > 0 {
			return 0, false
		}
		return 0, true
	case syntax.OpQuest:
		return maxMatchLen(re.Sub[0], limit)
	case syntax.OpRepeat:
		if re.Max < 0 {
			if minMatchLen(re.Sub[0]) > 0 {
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

func minMatchLen(re *syntax.Regexp) int64 {
	switch re.Op {
	case syntax.OpLiteral:
		return literalBytes(re)
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return 1
	case syntax.OpCapture:
		return minMatchLen(re.Sub[0])
	case syntax.OpConcat:
		var sum int64
		for _, sub := range re.Sub {
			sum += minMatchLen(sub)
		}
		return sum
	case syntax.OpAlternate:
		best := minMatchLen(re.Sub[0])
		for _, sub := range re.Sub[1:] {
			if length := minMatchLen(sub); length < best {
				best = length
			}
		}
		return best
	case syntax.OpStar, syntax.OpQuest:
		return 0
	case syntax.OpPlus:
		return minMatchLen(re.Sub[0])
	case syntax.OpRepeat:
		return int64(re.Min) * minMatchLen(re.Sub[0])
	default:
		return 0
	}
}

func literalBytes(re *syntax.Regexp) int64 {
	var total int64
	for _, r := range re.Rune {
		total += int64(utf8.RuneLen(r))
	}
	return total
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
