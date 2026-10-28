package inference

import (
	"encoding/json"
	"math"
)

// Opt-in bio-offset-consistency-v1 decoder contract for AstrLink Guard
// models (AstrLink Guard tools/offset-worker/contract.rs, sha256
// f9dffaa1180d8616d6a715c1dd4d28feaeca891dc1dbab9e02205fb0bfd761d5). Tokens
// whose offsets start before the furthest end seen so far share characters
// with an earlier token; the path may not open, close or switch an entity
// inside such a token. Entity ends are the union of their tokens' ends.

const (
	decoderContractField = "astrlink_guard_decoder_contract"
	decoderContract      = "bio-offset-consistency-v1"
)

var guardKinds = [10]string{"email", "phone", "account", "payment_card", "ip_address", "url", "common_secret", "private_address", "private_date", "private_person"}

// configuredContract reads an optional string contract field: absent or null
// disables it; any value other than want fails model loading.
func configuredContract(config []byte, field, want, invalid string) (bool, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(config, &document); err != nil || document == nil {
		return false, errCode("invalid_model_config")
	}
	raw, ok := document[field]
	if !ok || isNull(raw) {
		return false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value != want {
		return false, errCode(invalid)
	}
	return true, nil
}

func (d *decoder) withOffsetConsistency() error {
	if d.scheme != schemeBIO || len(d.labels) != 1+2*len(guardKinds) || d.labels[0].kind != tagOutside {
		return errCode("invalid_decoder_contract")
	}
	for index, kind := range guardKinds {
		begin, inside := d.labels[1+index*2], d.labels[2+index*2]
		if begin != (tag{tagBegin, kind, kind}) || inside != (tag{tagInside, kind, kind}) {
			return errCode("invalid_decoder_contract")
		}
	}
	d.offsetConsistency = true
	return nil
}

func (d *decoder) viterbiOffsetConsistent(logits []float32, offsets [][2]int) ([]int, error) {
	count, width := len(offsets), len(d.labels)
	if count == 0 {
		return nil, nil
	}
	for index, o := range offsets {
		if o[1] <= o[0] || (index > 0 && o[0] < offsets[index-1][0]) {
			return nil, errCode("invalid_offsets")
		}
	}
	negative := float32(math.Inf(-1))
	positive := float32(math.Inf(1))
	backpointers := make([]uint8, count*width)
	scores := append([]float32(nil), logits[:width]...)
	// Even label ids are I-* tags, which cannot open a path.
	for i := 2; i < width; i += 2 {
		scores[i] = negative
	}
	next := make([]float32, width)
	furthestEnd := offsets[0][1]
	for token := 1; token < count; token++ {
		overlap := offsets[token][0] < furthestEnd
		emissions := logits[token*width : (token+1)*width]
		anyFinite := false
		for target := range width {
			inside := target > 0 && target%2 == 0
			best, bestPrevious := negative, 0
			for previous, value := range scores {
				var allowed bool
				if overlap {
					allowed = (previous == 0 && target == 0) || (inside && (previous == target || previous+1 == target))
				} else {
					allowed = !inside || previous == target || previous+1 == target
				}
				if allowed && value > best {
					best, bestPrevious = value, previous
				}
			}
			// The emission is added after the argmax, like the float32
			// Python reference Viterbi.
			next[target] = best + emissions[target]
			backpointers[token*width+target] = uint8(bestPrevious)
			if next[target] == positive {
				return nil, errCode("nonfinite_path_score")
			}
			anyFinite = anyFinite || finite32(next[target])
		}
		if !anyFinite {
			return nil, errCode("nonfinite_path_score")
		}
		scores, next = next, scores
		furthestEnd = max(furthestEnd, offsets[token][1])
	}
	// The reference final-state tie rule: the last maximal label wins.
	last := 0
	for state := 1; state < width; state++ {
		if scores[state] >= scores[last] {
			last = state
		}
	}
	if !finite32(scores[last]) {
		return nil, errCode("nonfinite_path_score")
	}
	path := make([]int, count)
	path[count-1] = last
	for token := count - 1; token > 0; token-- {
		path[token-1] = int(backpointers[token*width+path[token]])
	}
	return path, nil
}

func decodeOffsetSpans(labels []tag, path []int, offsets [][2]int, probabilities []float32) ([]Span, error) {
	spans := []Span{}
	for index := 0; index < len(path); {
		label := labels[path[index]]
		switch label.kind {
		case tagOutside:
			index++
		case tagBegin:
			first := index
			index++
			for index < len(path) && labels[path[index]].kind == tagInside && labels[path[index]].source == label.source && labels[path[index]].canonical == label.canonical {
				index++
			}
			end := offsets[first][1]
			var sum float32
			for token := first; token < index; token++ {
				end = max(end, offsets[token][1])
				sum += probabilities[token]
			}
			if label.canonical == "" {
				return nil, errCode("invalid_decoder_contract")
			}
			spans = append(spans, Span{Start: offsets[first][0], End: end, Label: label.canonical, Score: sum / float32(index-first)})
		default:
			return nil, errCode("invalid_decoded_path")
		}
	}
	return spans, nil
}
