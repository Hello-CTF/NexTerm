package inference

import (
	"fmt"
	"math"
	"unicode/utf8"
)

const unigramUnknownPenalty = 10.0

// unigram is HF tokenizers' optimized Unigram Viterbi encoder.
type unigram struct {
	pieces       []string
	scores       []float64
	ids          map[string]uint32
	children     map[uint64]int32
	leaf         []bool
	minScore     float64
	unknownID    int
	byteFallback bool
}

type modelToken struct {
	id         uint32
	value      string
	start, end int
}

func newUnigram(vocabulary [][2]any, unknownID *int, byteFallback bool) (*unigram, error) {
	if len(vocabulary) == 0 || unknownID == nil || *unknownID < 0 || *unknownID >= len(vocabulary) {
		return nil, errUnsupportedTokenizer("unigram vocabulary")
	}
	u := &unigram{
		pieces: make([]string, len(vocabulary)), scores: make([]float64, len(vocabulary)),
		ids: make(map[string]uint32, len(vocabulary)), children: make(map[uint64]int32),
		leaf: []bool{false}, minScore: math.Inf(1), unknownID: *unknownID, byteFallback: byteFallback,
	}
	for id, entry := range vocabulary {
		piece, ok := entry[0].(string)
		score, scoreOK := entry[1].(float64)
		if !ok || !scoreOK || !utf8.ValidString(piece) {
			return nil, errUnsupportedTokenizer("unigram vocabulary")
		}
		u.pieces[id], u.scores[id] = piece, score
		u.ids[piece] = uint32(id)
		node := int32(0)
		for i := 0; i < len(piece); i++ {
			key := uint64(node)<<8 | uint64(piece[i])
			child, ok := u.children[key]
			if !ok {
				child = int32(len(u.leaf))
				u.leaf = append(u.leaf, false)
				u.children[key] = child
			}
			node = child
		}
		u.leaf[node] = true
		if score < u.minScore {
			u.minScore = score
		}
	}
	return u, nil
}

type bestPathNode struct {
	id       int
	score    float64
	startsAt int
}

func (u *unigram) encode(sentence string) []string {
	if sentence == "" {
		return nil
	}
	size := len(sentence)
	unknownScore := u.minScore - unigramUnknownPenalty
	best := make([]bestPathNode, size+1)
	for i := range best {
		best[i].startsAt = -1
	}
	for startsAt := 0; startsAt < size; {
		scoreTillHere := best[startsAt].score
		single := false
		_, characterLength := utf8.DecodeRuneInString(sentence[startsAt:])
		node := int32(0)
		for end := startsAt; end < size; end++ {
			child, ok := u.children[uint64(node)<<8|uint64(sentence[end])]
			if !ok {
				break
			}
			node = child
			if !u.leaf[node] {
				continue
			}
			keyPosition := end + 1
			id := int(u.ids[sentence[startsAt:keyPosition]])
			candidate := u.scores[id] + scoreTillHere
			target := &best[keyPosition]
			if target.startsAt < 0 || candidate > target.score {
				*target = bestPathNode{id: id, score: candidate, startsAt: startsAt}
			}
			if keyPosition-startsAt == characterLength {
				single = true
			}
		}
		if !single {
			target := &best[startsAt+characterLength]
			candidate := unknownScore + scoreTillHere
			if target.startsAt < 0 || candidate > target.score {
				*target = bestPathNode{id: u.unknownID, score: candidate, startsAt: startsAt}
			}
		}
		startsAt += characterLength
	}
	var results, unknown []string
	flush := func() {
		if len(unknown) > 0 {
			value := ""
			for i := len(unknown) - 1; i >= 0; i-- {
				value += unknown[i]
			}
			results = append(results, value)
			unknown = nil
		}
	}
	for end := size; end > 0; {
		node := best[end]
		if node.id == u.unknownID {
			unknown = append(unknown, sentence[node.startsAt:end])
		} else {
			flush()
			results = append(results, sentence[node.startsAt:end])
		}
		end = node.startsAt
	}
	flush()
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

func (u *unigram) tokenize(sentence string) []modelToken {
	pieces := u.encode(sentence)
	tokens := make([]modelToken, 0, len(pieces))
	offset := 0
	for _, piece := range pieces {
		end := offset + len(piece)
		id, ok := u.ids[piece]
		if !ok && u.byteFallback {
			bytes := make([]modelToken, 0, len(piece))
			for i := 0; i < len(piece); i++ {
				name := fmt.Sprintf("<0x%02X>", piece[i])
				byteID, found := u.ids[name]
				if !found {
					bytes = nil
					break
				}
				bytes = append(bytes, modelToken{byteID, name, offset, end})
			}
			if bytes != nil {
				tokens = append(tokens, bytes...)
				offset = end
				continue
			}
		}
		if !ok {
			id = uint32(u.unknownID)
		}
		tokens = append(tokens, modelToken{id, piece, offset, end})
		offset = end
	}
	return tokens
}
