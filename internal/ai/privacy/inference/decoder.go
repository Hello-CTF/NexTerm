package inference

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"unicode"
)

const maxLabelCount = 256

type tagScheme int

const (
	schemeBIO tagScheme = iota
	schemeBIOES
)

type tagKind int

const (
	tagOutside tagKind = iota
	tagBegin
	tagInside
	tagEnd
	tagSingle
)

type tag struct {
	kind      tagKind
	source    string
	canonical string // empty when the source label is mapped to null
}

// decoder is the HF token-classification decoder: constrained Viterbi over
// BIO/BIOES tags with zero transition biases, optionally under AstrLink
// Guard's bio-offset-consistency-v1 contract.
type decoder struct {
	labels            []tag
	scheme            tagScheme
	offsetConsistency bool
}

func errCode(code string) error { return errors.New(code) }

func canonicalKind(value string) bool {
	switch value {
	case "email", "phone", "account", "payment_card", "ip_address", "url", "common_secret", "private_address", "private_date", "private_person":
		return true
	}
	return false
}

func newHFDecoder(config []byte, scheme tagScheme, mapping map[string]*string) (*decoder, error) {
	if len(mapping) == 0 {
		return nil, errCode("invalid_model_mapping")
	}
	labels, err := parseLabels(config, scheme, mapping)
	if err != nil {
		return nil, err
	}
	return &decoder{labels: labels, scheme: scheme}, nil
}

func parseLabels(config []byte, scheme tagScheme, mapping map[string]*string) ([]tag, error) {
	for _, kind := range mapping {
		if kind != nil && !canonicalKind(*kind) {
			return nil, errCode("invalid_model_mapping")
		}
	}
	var document struct {
		ID2Label map[string]string `json:"id2label"`
	}
	if err := json.Unmarshal(config, &document); err != nil || document.ID2Label == nil {
		return nil, errCode("invalid_model_config")
	}
	if len(document.ID2Label) == 0 || len(document.ID2Label) > maxLabelCount {
		return nil, errCode("invalid_model_labels")
	}
	labels := make([]tag, 0, len(document.ID2Label))
	used := map[string]bool{}
	for index := range len(document.ID2Label) {
		label, ok := document.ID2Label[strconv.Itoa(index)]
		if !ok {
			return nil, errCode("invalid_model_labels")
		}
		parsed, err := parseTag(label, scheme, mapping, used)
		if err != nil {
			return nil, err
		}
		labels = append(labels, parsed)
	}
	outside := 0
	for _, label := range labels {
		if label.kind == tagOutside {
			outside++
		}
	}
	if outside != 1 || len(used) != len(mapping) {
		return nil, errCode("invalid_model_mapping")
	}
	if err := validateTagSet(labels, scheme); err != nil {
		return nil, err
	}
	return labels, nil
}

func parseTag(label string, scheme tagScheme, mapping map[string]*string, used map[string]bool) (tag, error) {
	if label == "O" {
		return tag{kind: tagOutside}, nil
	}
	if len(label) < 3 || (label[1] != '-' && label[1] != '_') {
		return tag{}, errCode("invalid_model_labels")
	}
	prefix, source := label[:1], label[2:]
	if len(source) > 128 {
		return tag{}, errCode("invalid_model_labels")
	}
	for _, r := range source {
		if unicode.IsControl(r) {
			return tag{}, errCode("invalid_model_labels")
		}
	}
	canonical, ok := mapping[source]
	if !ok {
		return tag{}, errCode("invalid_model_mapping")
	}
	used[source] = true
	t := tag{source: source}
	if canonical != nil {
		t.canonical = *canonical
	}
	switch {
	case prefix == "B":
		t.kind = tagBegin
	case prefix == "I":
		t.kind = tagInside
	case scheme == schemeBIOES && prefix == "E":
		t.kind = tagEnd
	case scheme == schemeBIOES && prefix == "S":
		t.kind = tagSingle
	default:
		return tag{}, errCode("invalid_model_labels")
	}
	return t, nil
}

func validateTagSet(labels []tag, scheme tagScheme) error {
	seen := map[string]*[4]bool{}
	for _, label := range labels {
		if label.kind == tagOutside {
			continue
		}
		entry := seen[label.source]
		if entry == nil {
			entry = &[4]bool{}
			seen[label.source] = entry
		}
		index := int(label.kind) - 1
		if entry[index] {
			return errCode("invalid_model_labels")
		}
		entry[index] = true
	}
	complete := [4]bool{true, true, scheme == schemeBIOES, scheme == schemeBIOES}
	if len(seen) == 0 {
		return errCode("invalid_model_labels")
	}
	for _, entry := range seen {
		if *entry != complete {
			return errCode("invalid_model_labels")
		}
	}
	return nil
}

func (d *decoder) labelCount() int { return len(d.labels) }

func validStart(scheme tagScheme, t tag) bool {
	if scheme == schemeBIO {
		return t.kind == tagOutside || t.kind == tagBegin
	}
	return t.kind == tagOutside || t.kind == tagBegin || t.kind == tagSingle
}

func validEnd(scheme tagScheme, t tag) bool {
	if scheme == schemeBIO {
		return t.kind == tagOutside || t.kind == tagBegin || t.kind == tagInside
	}
	return t.kind == tagOutside || t.kind == tagEnd || t.kind == tagSingle
}

// allowed reports whether a transition exists; all biases are zero for the
// HF adapter.
func (d *decoder) allowed(previous, next tag) bool {
	insideSame := (previous.kind == tagBegin || previous.kind == tagInside) && previous.source == next.source
	if d.scheme == schemeBIO {
		switch {
		case previous.kind == tagOutside:
			return next.kind == tagOutside || next.kind == tagBegin
		case previous.kind == tagBegin || previous.kind == tagInside:
			return (next.kind == tagInside && insideSame) || next.kind == tagOutside || next.kind == tagBegin
		}
		return false
	}
	switch {
	case previous.kind == tagOutside:
		return next.kind == tagOutside || next.kind == tagBegin || next.kind == tagSingle
	case previous.kind == tagBegin || previous.kind == tagInside:
		return (next.kind == tagInside || next.kind == tagEnd) && insideSame
	case previous.kind == tagEnd || previous.kind == tagSingle:
		return next.kind == tagOutside || next.kind == tagBegin || next.kind == tagSingle
	}
	return false
}

func finite32(value float32) bool {
	return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
}

func (d *decoder) viterbi(logits []float32, tokens int) ([]int, error) {
	count := d.labelCount()
	negative := float32(math.Inf(-1))
	scores := make([]float32, count)
	for state, label := range d.labels {
		scores[state] = negative
		if validStart(d.scheme, label) {
			scores[state] = logits[state]
		}
	}
	backpointers := make([]int, tokens*count)
	next := make([]float32, count)
	for token := 1; token < tokens; token++ {
		for state := range next {
			next[state] = negative
		}
		for target, nextLabel := range d.labels {
			for previous, previousScore := range scores {
				if !d.allowed(d.labels[previous], nextLabel) {
					continue
				}
				candidate := previousScore + logits[token*count+target]
				if candidate > next[target] {
					next[target] = candidate
					backpointers[token*count+target] = previous
				}
			}
		}
		scores, next = next, scores
	}
	// Iterator::max_by keeps the last of equal maxima.
	state, found := 0, false
	for candidate, label := range d.labels {
		if !validEnd(d.scheme, label) {
			continue
		}
		if !found || totalCompare(scores[candidate], scores[state]) >= 0 {
			state, found = candidate, true
		}
	}
	if !found || !finite32(scores[state]) {
		return nil, errCode("invalid_logits")
	}
	path := make([]int, tokens)
	path[tokens-1] = state
	for token := tokens - 1; token > 0; token-- {
		state = backpointers[token*count+state]
		path[token-1] = state
	}
	return path, nil
}

// totalCompare is f32::total_cmp for the values Viterbi produces.
func totalCompare(a, b float32) int {
	ai, bi := int32(math.Float32bits(a)), int32(math.Float32bits(b))
	ai ^= int32(uint32(ai>>31) >> 1)
	bi ^= int32(uint32(bi>>31) >> 1)
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	}
	return 0
}

func softmaxProbability(logits []float32, state int) float32 {
	maximum := float32(math.Inf(-1))
	for _, value := range logits {
		maximum = max(maximum, value)
	}
	if !finite32(maximum) {
		return 0
	}
	var denominator float32
	for _, value := range logits {
		denominator += float32(math.Exp(float64(value - maximum)))
	}
	if denominator == 0 || !finite32(denominator) {
		return 0
	}
	return min(max(float32(math.Exp(float64(logits[state]-maximum)))/denominator, 0), 1)
}

func (d *decoder) decode(logits []float32, offsets [][2]int) ([]Span, error) {
	count := d.labelCount()
	if len(logits) != len(offsets)*count {
		return nil, errCode("invalid_logits")
	}
	for _, value := range logits {
		if !finite32(value) {
			return nil, errCode("invalid_logits")
		}
	}
	if len(offsets) == 0 {
		return []Span{}, nil
	}
	var path []int
	var err error
	if d.offsetConsistency {
		path, err = d.viterbiOffsetConsistent(logits, offsets)
	} else {
		path, err = d.viterbi(logits, len(offsets))
	}
	if err != nil {
		return nil, err
	}
	probabilities := make([]float32, len(path))
	for token, state := range path {
		probabilities[token] = softmaxProbability(logits[token*count:(token+1)*count], state)
	}
	if d.offsetConsistency {
		return decodeOffsetSpans(d.labels, path, offsets, probabilities)
	}
	if d.scheme == schemeBIO {
		return decodeBIO(d.labels, path, offsets, probabilities)
	}
	return decodeBIOES(d.labels, path, offsets, probabilities)
}

func decodeBIO(labels []tag, path []int, offsets [][2]int, probabilities []float32) ([]Span, error) {
	spans := []Span{}
	for index := 0; index < len(path); {
		label := labels[path[index]]
		switch label.kind {
		case tagBegin:
			start := index
			index++
			for index < len(path) && labels[path[index]].kind == tagInside && labels[path[index]].source == label.source {
				index++
			}
			if label.canonical != "" {
				spans = pushSpan(spans, label.canonical, offsets[start:index], probabilities[start:index])
			}
		case tagOutside:
			index++
		default:
			return nil, errCode("invalid_decoded_path")
		}
	}
	return spans, nil
}

func decodeBIOES(labels []tag, path []int, offsets [][2]int, probabilities []float32) ([]Span, error) {
	spans := []Span{}
	for index := 0; index < len(path); {
		label := labels[path[index]]
		switch label.kind {
		case tagSingle:
			if label.canonical != "" {
				spans = pushSpan(spans, label.canonical, offsets[index:index+1], probabilities[index:index+1])
			}
			index++
		case tagBegin:
			start := index
			index++
			for index < len(path) && labels[path[index]].kind == tagInside && labels[path[index]].source == label.source {
				index++
			}
			if index >= len(path) || labels[path[index]].kind != tagEnd || labels[path[index]].source != label.source {
				return nil, errCode("invalid_decoded_path")
			}
			index++
			if label.canonical != "" {
				spans = pushSpan(spans, label.canonical, offsets[start:index], probabilities[start:index])
			}
		case tagOutside:
			index++
		default:
			return nil, errCode("invalid_decoded_path")
		}
	}
	return spans, nil
}

func pushSpan(spans []Span, canonical string, offsets [][2]int, probabilities []float32) []Span {
	start, end := -1, -1
	for _, o := range offsets {
		if o[0] < o[1] {
			start = o[0]
			break
		}
	}
	for i := len(offsets) - 1; i >= 0; i-- {
		if offsets[i][0] < offsets[i][1] {
			end = offsets[i][1]
			break
		}
	}
	if start < 0 || end < 0 || start >= end {
		return spans
	}
	var sum float32
	for _, p := range probabilities {
		sum += p
	}
	return append(spans, Span{Start: start, End: end, Label: canonical, Score: sum / float32(len(probabilities))})
}
