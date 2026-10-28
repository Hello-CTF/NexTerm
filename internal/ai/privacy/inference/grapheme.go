// Ported from unicode-segmentation 1.13.3 (MIT OR Apache-2.0) src/grapheme.rs.
// Modified for NexTerm; see ../third_party/ASTRLINK_NOTICE.md.

package inference

import (
	"slices"
	"sort"
	"unicode/utf8"
)

// Extended grapheme clusters (UAX #29) as implemented by unicode-segmentation
// 1.13.3, which HF tokenizers uses for the precompiled normalizer.

type graphemeCategory uint8

const (
	gcAny graphemeCategory = iota
	gcCR
	gcControl
	gcExtend
	gcExtendedPictographic
	gcInCBConsonant
	gcL
	gcLF
	gcLV
	gcLVT
	gcPrepend
	gcRegionalIndicator
	gcSpacingMark
	gcT
	gcV
	gcZWJ
)

type graphemeRange struct {
	low, high rune
	category  graphemeCategory
}

func graphemeCategoryOf(r rune) graphemeCategory {
	if r <= 0x7e {
		switch {
		case r >= 0x20:
			return gcAny
		case r == '\n':
			return gcLF
		case r == '\r':
			return gcCR
		default:
			return gcControl
		}
	}
	i := sort.Search(len(graphemeCategoryTable), func(i int) bool { return graphemeCategoryTable[i].high >= r })
	if i < len(graphemeCategoryTable) && graphemeCategoryTable[i].low <= r {
		return graphemeCategoryTable[i].category
	}
	return gcAny
}

func incbExtend(r rune) bool {
	i := sort.Search(len(incbExtendTable), func(i int) bool { return incbExtendTable[i][1] >= r })
	return i < len(incbExtendTable) && incbExtendTable[i][0] <= r
}

type pairResult uint8

const (
	pairNotBreak pairResult = iota
	pairBreak
	pairExtended
	pairInCBConsonant
	pairEmoji
	pairRegional
)

func checkPair(before, after graphemeCategory) pairResult {
	switch {
	case before == gcCR && after == gcLF:
		return pairNotBreak // GB3
	case before == gcControl || before == gcCR || before == gcLF:
		return pairBreak // GB4
	case after == gcControl || after == gcCR || after == gcLF:
		return pairBreak // GB5
	case before == gcL && (after == gcL || after == gcV || after == gcLV || after == gcLVT):
		return pairNotBreak // GB6
	case (before == gcLV || before == gcV) && (after == gcV || after == gcT):
		return pairNotBreak // GB7
	case (before == gcLVT || before == gcT) && after == gcT:
		return pairNotBreak // GB8
	case after == gcExtend || after == gcZWJ:
		return pairNotBreak // GB9
	case after == gcSpacingMark:
		return pairExtended // GB9a
	case before == gcPrepend:
		return pairExtended // GB9b
	case after == gcInCBConsonant:
		return pairInCBConsonant // GB9c
	case before == gcZWJ && after == gcExtendedPictographic:
		return pairEmoji // GB11
	case before == gcRegionalIndicator && after == gcRegionalIndicator:
		return pairRegional // GB12, GB13
	default:
		return pairBreak // GB999
	}
}

// graphemeBoundary decides whether an extended grapheme boundary lies at a
// character boundary i strictly inside s. regionalRun counts the consecutive
// regional indicators ending at i; the caller tracks it forward like
// unicode-segmentation's ris_count so GB12/GB13 stay linear.
func graphemeBoundary(s string, i, regionalRun int) bool {
	beforeRune, _ := utf8.DecodeLastRuneInString(s[:i])
	afterRune, _ := utf8.DecodeRuneInString(s[i:])
	switch checkPair(graphemeCategoryOf(beforeRune), graphemeCategoryOf(afterRune)) {
	case pairNotBreak, pairExtended:
		return false
	case pairInCBConsonant:
		linkers := 0
		for prefix := s[:i]; prefix != ""; {
			r, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
			switch {
			case slices.Contains(incbLinkers, r):
				linkers++
			case incbExtend(r):
			default:
				return !(linkers > 0 && graphemeCategoryOf(r) == gcInCBConsonant)
			}
		}
		return true
	case pairEmoji:
		// The character before i is the ZWJ; skip Extend back to a pictograph.
		prefix := s[:i]
		_, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
		for prefix != "" {
			r, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
			switch graphemeCategoryOf(r) {
			case gcExtend:
			case gcExtendedPictographic:
				return false
			default:
				return true
			}
		}
		return true
	case pairRegional:
		return regionalRun%2 == 0
	default:
		return true
	}
}

// forEachGrapheme yields each extended grapheme cluster of s in order and
// stops when yield returns false.
func forEachGrapheme(s string, yield func(string) bool) {
	start, regionalRun := 0, 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if graphemeCategoryOf(r) == gcRegionalIndicator {
			regionalRun++
		} else {
			regionalRun = 0
		}
		if i == len(s) || graphemeBoundary(s, i, regionalRun) {
			if !yield(s[start:i]) {
				return
			}
			start = i
		}
	}
}
