package inference

import (
	"context"
	"encoding/binary"
	"unicode/utf8"
)

// precompiled is SentencePiece's precompiled_charsmap normalizer as
// implemented by HF tokenizers (spm_precompiled 0.1.4): a double-array trie
// whose values index NUL-terminated replacements.
type precompiled struct {
	trie       []uint32
	normalized string
}

func newPrecompiled(charsmap []byte) (*precompiled, error) {
	if len(charsmap) < 4 {
		return nil, errUnsupportedTokenizer("precompiled_charsmap")
	}
	size := binary.LittleEndian.Uint32(charsmap) / 4
	rest := charsmap[4:]
	if uint64(len(rest)) < uint64(size)*4 || size == 0 {
		return nil, errUnsupportedTokenizer("precompiled_charsmap")
	}
	trie := make([]uint32, size)
	for i := range trie {
		trie[i] = binary.LittleEndian.Uint32(rest[4*i:])
	}
	normalized := rest[4*int(size):]
	if !utf8.Valid(normalized) {
		return nil, errUnsupportedTokenizer("precompiled_charsmap")
	}
	return &precompiled{trie: trie, normalized: string(normalized)}, nil
}

func unitOffset(unit uint32) uint32 { return (unit >> 10) << ((unit & (1 << 9)) >> 6) }

// firstPrefix returns the value of the shortest trie key prefixing key, as
// spm_precompiled's transform uses only the first common-prefix result.
func (p *precompiled) firstPrefix(key string) (int, bool, error) {
	position := uint32(0)
	position ^= unitOffset(p.trie[0])
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c == 0 {
			break
		}
		position ^= uint32(c)
		if int(position) >= len(p.trie) {
			return 0, false, errTokenizer
		}
		unit := p.trie[position]
		if unit&((1<<31)|0xff) != uint32(c) {
			return 0, false, nil
		}
		position ^= unitOffset(unit)
		if (unit>>8)&1 == 1 {
			if int(position) >= len(p.trie) {
				return 0, false, errTokenizer
			}
			return int(p.trie[position] & ((1 << 31) - 1)), true, nil
		}
	}
	return 0, false, nil
}

func (p *precompiled) transform(chunk string) (string, bool, error) {
	index, ok, err := p.firstPrefix(chunk)
	if err != nil || !ok {
		return "", false, err
	}
	if index > len(p.normalized) {
		return "", false, errTokenizer
	}
	end := index
	for end < len(p.normalized) && p.normalized[end] != 0 {
		end++
	}
	if !charBoundary(p.normalized, index) || !charBoundary(p.normalized, end) {
		return "", false, errTokenizer
	}
	return p.normalized[index:end], true, nil
}

// precompiledReplace mirrors tokenizers' normalizers::precompiled::replace.
func precompiledReplace(changes []charChange, old, replacement string) []charChange {
	diff := utf8.RuneCountInString(replacement) - utf8.RuneCountInString(old)
	for _, r := range replacement {
		changes = append(changes, charChange{r, 0})
	}
	switch {
	case diff > 0:
		for i := len(changes) - 1; i >= 0 && i >= len(changes)-diff; i-- {
			changes[i].change = 1
		}
	case diff < 0:
		if len(changes) > 0 {
			changes[len(changes)-1].change += diff
		}
	}
	return changes
}

// normalize reproduces the grapheme-then-character lookup of HF tokenizers,
// including its use of the shortest matching prefix for a whole grapheme.
// It checks ctx every cancelCheckInterval graphemes.
func (p *precompiled) normalize(ctx context.Context, n *normalizedString) error {
	changes := make([]charChange, 0, len(n.normalized))
	modified := false
	var failure error
	count := 0
	forEachGrapheme(n.normalized, func(grapheme string) bool {
		if count++; count%cancelCheckInterval == 0 {
			if failure = ctx.Err(); failure != nil {
				return false
			}
		}
		if len(grapheme) < 6 {
			replacement, ok, err := p.transform(grapheme)
			if err != nil {
				failure = err
				return false
			}
			if ok {
				modified = true
				changes = precompiledReplace(changes, grapheme, replacement)
				return true
			}
		}
		for b, r := range grapheme {
			part := grapheme[b : b+utf8.RuneLen(r)]
			replacement, ok, err := p.transform(part)
			if err != nil {
				failure = err
				return false
			}
			if ok {
				modified = true
				changes = precompiledReplace(changes, part, replacement)
			} else {
				changes = append(changes, charChange{r, 0})
			}
		}
		return true
	})
	if failure != nil {
		return failure
	}
	if modified {
		return n.transform(changes, 0)
	}
	return nil
}
